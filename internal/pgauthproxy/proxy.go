// Package pgauthproxy is test plumbing for the credential rotation proofs of S04-H (docs/S04H_SCOPE.md H3). It is never linked into a
// binary of the platform.
//
// The throwaway PostgreSQL servers of the qualification (the local container, the CI service) authenticate by "trust": whatever password a
// client sends is accepted, so a changed password cannot be shown to refuse the old one. This proxy puts the check back in front of such a
// server. It speaks the PostgreSQL wire protocol to the client, runs the SCRAM-SHA-256 exchange against the verifier PostgreSQL itself
// stores for the role (pg_authid.rolpassword, read at every connection, so ALTER ROLE ... PASSWORD changes the answer at once), and only
// then relays the connection to the real server. It is what "host all all scram-sha-256" does, written down for a server that does not have
// it: the verifier, the roles and the sessions are PostgreSQL's; the decision to let a new connection in is the proxy's. A test that uses it
// must say so.
package pgauthproxy

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Verifiers says which verifier a role has: the admin pool reads it from the server (superuser only).
type Verifiers interface {
	Verifier(ctx context.Context, role string) (string, error)
}

// Admin reads the verifiers through a superuser pool.
type Admin struct{ Pool *pgxpool.Pool }

func (a Admin) Verifier(ctx context.Context, role string) (string, error) {
	var v *string
	if err := a.Pool.QueryRow(ctx, "SELECT rolpassword FROM pg_authid WHERE rolname = $1", role).Scan(&v); err != nil {
		return "", err
	}
	if v == nil {
		return "", errors.New("no password")
	}
	return *v, nil
}

// Proxy is a running password-enforcing front of an upstream server.
type Proxy struct {
	// Addr is host:port of the proxy.
	Addr string
	// Accepted and Refused count the connections that passed and failed the password check.
	Accepted, Refused atomic.Int64
	ln                net.Listener
	wg                sync.WaitGroup
}

// Start serves on a loopback port until the test ends.
func Start(t testing.TB, upstream string, v Verifiers) *Proxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxy{Addr: ln.Addr().String(), ln: ln}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				p.serve(c, upstream, v)
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close(); p.wg.Wait() })
	return p
}

func (p *Proxy) serve(client net.Conn, upstream string, v Verifiers) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	backend := pgproto3.NewBackend(client, client)
	msg, err := backend.ReceiveStartupMessage()
	if err != nil {
		return
	}
	startup, ok := msg.(*pgproto3.StartupMessage)
	if !ok { // SSL and GSS requests: the proxy offers neither
		return
	}
	role := startup.Parameters["user"]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	verifier, err := v.Verifier(ctx, role)
	if err != nil || authenticate(backend, verifier) != nil {
		p.Refused.Add(1)
		_ = send(backend, &pgproto3.ErrorResponse{Severity: "FATAL", SeverityUnlocalized: "FATAL", Code: "28P01", Message: fmt.Sprintf("password authentication failed for user %q", role)})
		return
	}
	p.Accepted.Add(1)
	server, err := net.DialTimeout("tcp", upstream, 5*time.Second)
	if err != nil {
		return
	}
	defer server.Close()
	// The upstream trusts: it answers the startup message with AuthenticationOk, which the client has already been given.
	encoded, err := startup.Encode(nil)
	if err != nil {
		return
	}
	if _, err := server.Write(encoded); err != nil {
		return
	}
	var ok9 [9]byte
	if _, err := io.ReadFull(server, ok9[:]); err != nil || ok9[0] != 'R' {
		return
	}
	if err := send(backend, &pgproto3.AuthenticationOk{}); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(server, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, server); done <- struct{}{} }()
	<-done
}

// send writes one message and flushes it.
func send(b *pgproto3.Backend, m pgproto3.BackendMessage) error {
	b.Send(m)
	return b.Flush()
}

// authenticate runs SCRAM-SHA-256 (RFC 5802, RFC 7677 with PostgreSQL's empty user name) against the stored verifier
// "SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>". It sends nothing on success but the final message; the caller sends Ok.
func authenticate(b *pgproto3.Backend, verifier string) error {
	rest, ok := strings.CutPrefix(verifier, "SCRAM-SHA-256$")
	if !ok {
		return errors.New("the verifier is not SCRAM-SHA-256")
	}
	params, keys, ok := strings.Cut(rest, "$")
	iterSalt := strings.SplitN(params, ":", 2)
	storedServer := strings.SplitN(keys, ":", 2)
	if !ok || len(iterSalt) != 2 || len(storedServer) != 2 {
		return errors.New("malformed verifier")
	}
	iterations, err := strconv.Atoi(iterSalt[0])
	if err != nil || iterations < 1 {
		return errors.New("malformed verifier")
	}
	storedKey, err1 := base64.StdEncoding.DecodeString(storedServer[0])
	serverKey, err2 := base64.StdEncoding.DecodeString(storedServer[1])
	if err1 != nil || err2 != nil {
		return errors.New("malformed verifier")
	}
	if err := send(b, &pgproto3.AuthenticationSASL{AuthMechanisms: []string{"SCRAM-SHA-256"}}); err != nil {
		return err
	}
	b.SetAuthType(pgproto3.AuthTypeSASL)
	m, err := b.Receive()
	if err != nil {
		return err
	}
	initial, ok := m.(*pgproto3.SASLInitialResponse)
	if !ok || initial.AuthMechanism != "SCRAM-SHA-256" {
		return errors.New("unexpected message")
	}
	clientFirst := string(initial.Data)
	bare, ok := strings.CutPrefix(clientFirst, "n,,")
	if !ok {
		return errors.New("unsupported channel binding")
	}
	var cnonce string
	for _, f := range strings.Split(bare, ",") {
		if v, ok := strings.CutPrefix(f, "r="); ok {
			cnonce = v
		}
	}
	if cnonce == "" {
		return errors.New("no nonce")
	}
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	nonce := cnonce + base64.StdEncoding.EncodeToString(raw)
	serverFirst := "r=" + nonce + ",s=" + iterSalt[1] + ",i=" + strconv.Itoa(iterations)
	if err := send(b, &pgproto3.AuthenticationSASLContinue{Data: []byte(serverFirst)}); err != nil {
		return err
	}
	b.SetAuthType(pgproto3.AuthTypeSASLContinue)
	m, err = b.Receive()
	if err != nil {
		return err
	}
	final, ok := m.(*pgproto3.SASLResponse)
	if !ok {
		return errors.New("unexpected message")
	}
	clientFinal := string(final.Data)
	withoutProof, proofB64, ok := strings.Cut(clientFinal, ",p=")
	if !ok || !strings.Contains(withoutProof, "r="+nonce) {
		return errors.New("bad client-final")
	}
	proof, err := base64.StdEncoding.DecodeString(proofB64)
	if err != nil || len(proof) != sha256.Size {
		return errors.New("bad proof")
	}
	authMessage := bare + "," + serverFirst + "," + withoutProof
	mac := hmac.New(sha256.New, storedKey)
	mac.Write([]byte(authMessage))
	signature := mac.Sum(nil)
	clientKey := make([]byte, len(proof))
	for i := range proof {
		clientKey[i] = proof[i] ^ signature[i]
	}
	if got := sha256.Sum256(clientKey); !hmac.Equal(got[:], storedKey) {
		return errors.New("wrong password")
	}
	mac = hmac.New(sha256.New, serverKey)
	mac.Write([]byte(authMessage))
	return send(b, &pgproto3.AuthenticationSASLFinal{Data: []byte("v=" + base64.StdEncoding.EncodeToString(mac.Sum(nil)))})
}
