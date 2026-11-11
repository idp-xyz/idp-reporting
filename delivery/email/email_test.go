package email_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"mime"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/delivery/email"
	r "idp.local/reporting/reporting"
)

// The known values of these tests: what a mail must carry, and what no error, log or wrong place may.
const (
	testName     = "应收账款日报 AR daily"
	testFrom     = "reporting@platform.example"
	testTo       = "zhang.san@example.com"
	testPassword = "smtp-PASSWORD-Zq7-never-shown"
	testUser     = "reporting-sender"
	testRow      = "customer_a-Zq7" // a value of the report, which no mail may carry
)

func owner() r.Identity { return r.Identity{Tenant: "t", Issuer: "https://idp.example", Subject: "s"} }

func notification() r.Notification {
	o := owner()
	return r.Notification{Event: r.NotificationEvent, RunID: strings.Repeat("a", 32), ScheduleID: strings.Repeat("b", 32), Tenant: o.Tenant, Issuer: o.Issuer, Subject: o.Subject,
		Integration: "p", DueAt: "2026-11-11T07:30:00Z", FinishedAt: "2026-11-11T07:30:41Z", State: r.RunSucceeded, SnapshotID: strings.Repeat("c", 32), ArtifactID: strings.Repeat("d", 32), Format: "xlsx", Attempt: 1, SentAt: "2026-11-11T07:30:42Z"}
}

// received is one message the fake server took: the envelope, the data, and whether TLS and authentication were on.
type received struct {
	From string
	To   []string
	Data []byte
	TLS  bool
	User string
}

// fakeSMTP is an SMTP server for one test: 127.0.0.1, enough of the protocol for EHLO, STARTTLS (when it has a certificate), AUTH PLAIN
// (when it has users), MAIL, RCPT, DATA and QUIT. It records every message, every MAIL command received in the clear while it offered
// STARTTLS, and refuses what the test tells it to.
type fakeSMTP struct {
	ln         net.Listener
	tls        *tls.Config
	users      map[string]string
	rejectRcpt string // a recipient answered 550
	dataReply  string // the reply to a message's data, "" for 250
	silent     bool   // never greets: the client's timeout must end the session
	done       chan struct{}
	mu         sync.Mutex
	messages   []received
	clearMail  int
}

func startFake(t *testing.T, f *fakeSMTP) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.ln, f.done = ln, make(chan struct{})
	t.Cleanup(func() { close(f.done); _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) hostPort() (string, int) {
	a := f.ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

func (f *fakeSMTP) taken() ([]received, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]received(nil), f.messages...), f.clearMail
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	if f.silent {
		<-f.done
		return
	}
	var secure bool
	var user, from string
	var to []string
	reply := func(s string) { _, _ = fmt.Fprintf(conn, "%s\r\n", s) }
	reply("220 fake ESMTP")
	br := bufio.NewReader(conn)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			lines := []string{"fake greets you"}
			if f.tls != nil && !secure {
				lines = append(lines, "STARTTLS")
			}
			if f.users != nil {
				lines = append(lines, "AUTH PLAIN")
			}
			for i, l := range lines {
				if i == len(lines)-1 {
					reply("250 " + l)
				} else {
					reply("250-" + l)
				}
			}
		case "STARTTLS":
			if f.tls == nil || secure {
				reply("502 not offered")
				continue
			}
			reply("220 go ahead")
			tc := tls.Server(conn, f.tls)
			if tc.Handshake() != nil {
				return
			}
			conn, br, secure = tc, bufio.NewReader(tc), true
		case "AUTH":
			mech, initial, _ := strings.Cut(arg, " ")
			raw, _ := base64.StdEncoding.DecodeString(initial)
			parts := bytes.Split(raw, []byte{0})
			if !strings.EqualFold(mech, "PLAIN") || len(parts) != 3 {
				reply("504 mechanism not supported")
				continue
			}
			if pw, ok := f.users[string(parts[1])]; !ok || pw != string(parts[2]) {
				reply("535 authentication failed")
				continue
			}
			user = string(parts[1])
			reply("235 authenticated")
		case "MAIL":
			if f.tls != nil && !secure {
				f.mu.Lock()
				f.clearMail++
				f.mu.Unlock()
			}
			from, to = strings.TrimSuffix(strings.TrimPrefix(arg, "FROM:<"), ">"), nil
			reply("250 sender ok")
		case "RCPT":
			addr := strings.TrimSuffix(strings.TrimPrefix(arg, "TO:<"), ">")
			if addr == f.rejectRcpt {
				reply("550 no such user")
				continue
			}
			to = append(to, addr)
			reply("250 recipient ok")
		case "DATA":
			reply("354 end with <CRLF>.<CRLF>")
			var data bytes.Buffer
			for {
				l, err := br.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, "."))
			}
			if f.dataReply != "" {
				reply(f.dataReply)
				continue
			}
			f.mu.Lock()
			f.messages = append(f.messages, received{From: from, To: to, Data: data.Bytes(), TLS: secure, User: user})
			f.mu.Unlock()
			reply("250 queued")
		case "QUIT":
			reply("221 bye")
			return
		case "RSET":
			from, to = "", nil
			reply("250 ok")
		case "NOOP":
			reply("250 ok")
		default:
			reply("502 command not implemented")
		}
	}
}

// serverTLS is a certificate for 127.0.0.1 of the fake's own, and the client configuration that trusts it alone.
func serverTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake-smtp"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: priv}}, MinVersion: tls.VersionTLS12},
		&tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}
}

// config is a valid configuration to the fake over plain loopback SMTP, with the one recipient and the name; the tests change one thing in it.
func config(f *fakeSMTP) email.Config {
	host, port := f.hostPort()
	return email.Config{Host: host, Port: port, From: testFrom, LoopbackPlain: true, Timeout: 5 * time.Second, ConsoleURL: "http://127.0.0.1:8443",
		Recipients: func(r.Identity) []string { return []string{testTo} },
		Names:      func(context.Context, r.Identity, string) (string, error) { return testName, nil },
		Now:        func() time.Time { return time.Date(2026, 11, 11, 7, 30, 42, 0, time.UTC) }}
}

// parsed is a received message read back: its headers and its decoded body.
func parsed(t *testing.T, m received) (mail.Header, string) {
	t.Helper()
	msg, err := mail.ReadMessage(bytes.NewReader(m.Data))
	if err != nil {
		t.Fatalf("%v\n%s", err, m.Data)
	}
	if msg.Header.Get("Content-Transfer-Encoding") != "base64" || msg.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("%v", msg.Header)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(readAll(t, msg)), ""))
	if err != nil {
		t.Fatal(err)
	}
	return msg.Header, string(raw)
}

func readAll(t *testing.T, msg *mail.Message) string {
	t.Helper()
	var b bytes.Buffer
	if _, err := b.ReadFrom(msg.Body); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func decodedSubject(t *testing.T, h mail.Header) string {
	t.Helper()
	dec := new(mime.WordDecoder)
	s, err := dec.DecodeHeader(h.Get("Subject"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// An address is one bare addr-spec of printable ASCII; a host a name or an IP without a port; the console URL https (or http to loopback with
// the option); plain SMTP only to a loopback IP address with the option; a username only with its password; the timeout 1..30 s. No refusal
// names a value.
func TestTheConfigurationIsRefusedOutsideItsBounds(t *testing.T) {
	for _, c := range []struct {
		address string
		ok      bool
	}{
		{"a@b", true}, {"zhang.san@example.com", true}, {"user+tag@sub.example.co.uk", true}, {"UPPER@EXAMPLE.COM", true}, {strings.Repeat("a", 242) + "@example.com", true},
		{"", false}, {"a@", false}, {"@example.com", false}, {"a b@example.com", false}, {"Zhang San <a@example.com>", false}, {"<a@example.com>", false},
		{"a@example.com (comment)", false}, {"a@example.com, b@example.com", false}, {"a@example..com", false}, {"a@example.com\r\nBcc: b@example.com", false},
		{"张三@example.com", false}, {"\"quoted\"@example.com", false}, {strings.Repeat("a", 243) + "@example.com", false},
	} {
		if got := email.ValidAddress(c.address); got != c.ok {
			t.Errorf("address %q: %v", c.address, got)
		}
	}
	for _, c := range []struct {
		host string
		ok   bool
	}{
		{"smtp.example.com", true}, {"mail", true}, {"127.0.0.1", true}, {"::1", true}, {"a-b.example", true}, {"smtp.example.com.", true},
		{"", false}, {"smtp.example.com:587", false}, {"-a.example", false}, {"a_b.example", false}, {"a..example", false}, {"smtp.example/path", false}, {strings.Repeat("a", 254), false}, {"a b", false},
	} {
		if got := email.ValidHost(c.host); got != c.ok {
			t.Errorf("host %q: %v", c.host, got)
		}
	}
	for _, c := range []struct {
		url      string
		loopback bool
		ok       bool
	}{
		{"https://reporting.example.com", false, true}, {"https://reporting.example.com:8443/base", false, true}, {"http://127.0.0.1:8443", true, true}, {"http://[::1]:8443", true, true},
		{"http://127.0.0.1:8443", false, false}, {"http://localhost:8443", true, false}, {"http://reporting.example.com", true, false}, {"https://u:p@reporting.example.com", false, false},
		{"https://reporting.example.com/?x=1", false, false}, {"https://reporting.example.com/#frag", false, false}, {"ftp://reporting.example.com", false, false}, {"reporting.example.com", false, false}, {"", false, false},
	} {
		if got := email.ValidConsoleURL(c.url, c.loopback); got != c.ok {
			t.Errorf("console %q loopback=%v: %v", c.url, c.loopback, got)
		}
	}
	f := startFake(t, &fakeSMTP{})
	if _, e := email.New(config(f)); e != nil {
		t.Fatal(e)
	}
	secure := config(f)
	secure.Host, secure.Port, secure.StartTLS, secure.LoopbackPlain, secure.ConsoleURL = "smtp.example.com", 0, true, false, "https://reporting.example.com"
	if _, e := email.New(secure); e != nil {
		t.Fatal(e)
	}
	for name, change := range map[string]func(*email.Config){
		"plain SMTP to a host that is not loopback": func(c *email.Config) { c.Host = "smtp.example.com" },
		"plain SMTP to a name that is loopback":     func(c *email.Config) { c.Host = "localhost" },
		"plain SMTP to loopback without the option": func(c *email.Config) { c.LoopbackPlain = false },
		"no host":                    func(c *email.Config) { c.Host = "" },
		"a host with a port":         func(c *email.Config) { c.Host = "127.0.0.1:25" },
		"a port over 65535":          func(c *email.Config) { c.Port = 65536 },
		"a negative port":            func(c *email.Config) { c.Port = -1 },
		"no from":                    func(c *email.Config) { c.From = "" },
		"a from with a display name": func(c *email.Config) { c.From = "Reporting <" + testFrom + ">" },
		"a console URL that is not loopback http":   func(c *email.Config) { c.ConsoleURL = "http://reporting.example.com" },
		"a console URL with a query":                func(c *email.Config) { c.ConsoleURL = "https://reporting.example.com/?a=1" },
		"no console URL":                            func(c *email.Config) { c.ConsoleURL = "" },
		"a timeout under a second":                  func(c *email.Config) { c.Timeout = 500 * time.Millisecond },
		"a timeout over 30 seconds":                 func(c *email.Config) { c.Timeout = 31 * time.Second },
		"no recipients function":                    func(c *email.Config) { c.Recipients = nil },
		"a username without a password":             func(c *email.Config) { c.Username = testUser },
		"a password without a username":             func(c *email.Config) { c.Password = testPassword },
		"a username with a space":                   func(c *email.Config) { c.Username, c.Password = "a user", testPassword },
		"a password with a control character":       func(c *email.Config) { c.Username, c.Password = testUser, "pw\n"+testPassword },
		"a password over its bound":                 func(c *email.Config) { c.Username, c.Password = testUser, strings.Repeat("p", 1025) },
		"STARTTLS with a console URL of plain http": func(c *email.Config) { c.StartTLS, c.LoopbackPlain = true, false },
	} {
		c := config(f)
		change(&c)
		m, e := email.New(c)
		if !errors.Is(e, email.ErrConfig) || m != nil {
			t.Errorf("%s: %v", name, e)
			continue
		}
		for _, value := range []string{testPassword, testUser, testFrom, "smtp.example.com", "reporting.example.com"} {
			if strings.Contains(e.Error(), value) {
				t.Errorf("%s: the refusal carries %q", name, value)
			}
		}
	}
}

// A mail is one text/plain message to the owner's address, from the configured sender, with the schedule's name and the outcome in its subject
// and the name, the occurrence, the outcome with its code, the ids and the console's link in its body; headers name the run and the event. No
// attachment, no second part, nothing of the report.
func TestAMailCarriesTheNotificationForAPersonAndNothingOfTheReport(t *testing.T) {
	f := startFake(t, &fakeSMTP{})
	m, e := email.New(config(f))
	if e != nil {
		t.Fatal(e)
	}
	n := notification()
	if res := m.Deliver(context.Background(), n); res.Outcome != r.DeliveryOutcomeDelivered || res.Status != 250 {
		t.Fatalf("%+v", res)
	}
	got, _ := f.taken()
	if len(got) != 1 || got[0].From != testFrom || len(got[0].To) != 1 || got[0].To[0] != testTo || got[0].TLS || got[0].User != "" {
		t.Fatalf("%+v", got)
	}
	h, body := parsed(t, got[0])
	if h.Get("From") != testFrom || h.Get("To") != testTo || h.Get(email.DeliveryHeader) != n.RunID || h.Get(email.EventHeader) != r.NotificationEvent ||
		h.Get("Auto-Submitted") != "auto-generated" || h.Get("MIME-Version") != "1.0" || h.Get("Date") != "Wed, 11 Nov 2026 07:30:42 +0000" || !strings.Contains(h.Get("Message-ID"), n.RunID) {
		t.Fatalf("%v", h)
	}
	if subject := decodedSubject(t, h); subject != "[IDP Reporting] 定时报表 "+testName+" · 成功" {
		t.Fatalf("subject %q", subject)
	}
	for _, want := range []string{testName, "2026-11-11T07:30:00Z", "2026-11-11T07:30:41Z", "成功（succeeded）", "xlsx", n.RunID, n.ScheduleID, "http://127.0.0.1:8443/console/#schedules", "没有附件"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body lacks %q:\n%s", want, body)
		}
	}
	// What is not there: the raw message holds the name only encoded, no boundary, no attachment, no snapshot or artifact id (the console shows
	// them), and no value of the report, which the deliverer never sees.
	raw := string(got[0].Data)
	for _, absent := range []string{testName, "multipart", "attachment", "boundary", n.SnapshotID, n.ArtifactID, testRow, "customer"} {
		if strings.Contains(raw, absent) || strings.Contains(body, absent) && absent != testName {
			t.Errorf("the mail carries %q", absent)
		}
	}
	// A failed run says so with its code; a name the store cannot give leaves the schedule's id in its place; a refused and a cancelled run
	// have their own words.
	c := config(f)
	c.Names = func(context.Context, r.Identity, string) (string, error) { return "", errors.New("no such schedule") }
	m, _ = email.New(c)
	failed := n
	failed.State, failed.ErrorCode, failed.SnapshotID, failed.ArtifactID = r.RunFailed, "QUOTA_EXCEEDED", "", ""
	if res := m.Deliver(context.Background(), failed); res.Outcome != r.DeliveryOutcomeDelivered {
		t.Fatalf("%+v", res)
	}
	got, _ = f.taken()
	h, body = parsed(t, got[1])
	if subject := decodedSubject(t, h); subject != "[IDP Reporting] 定时报表 "+n.ScheduleID+" · 失败" {
		t.Fatalf("subject %q", subject)
	}
	if !strings.Contains(body, "失败（failed，QUOTA_EXCEEDED）") || !strings.Contains(body, "计划名称："+n.ScheduleID) || strings.Contains(body, testName) {
		t.Fatalf("%s", body)
	}
	c.Names = nil
	m, _ = email.New(c)
	for state, word := range map[string]string{r.RunRefused: "被拒绝", r.RunCancelled: "已取消"} {
		other := n
		other.State = state
		m.Deliver(context.Background(), other)
		got, _ = f.taken()
		if h, _ := parsed(t, got[len(got)-1]); !strings.HasSuffix(decodedSubject(t, h), " · "+word) {
			t.Errorf("%s: %q", state, decodedSubject(t, h))
		}
	}
	// A name of a person's choosing reaches no header as it is: the subject is encoded whole.
	c.Names = func(context.Context, r.Identity, string) (string, error) { return "x\r\nBcc: spy@example.com", nil }
	m, _ = email.New(c)
	m.Deliver(context.Background(), n)
	got, _ = f.taken()
	if h, _ := parsed(t, got[len(got)-1]); h.Get("Bcc") != "" || strings.Contains(string(got[len(got)-1].Data), "spy@example.com") {
		t.Fatal("a name reached the headers")
	}
}

// The recipients are the owner's addresses as the bindings name them, distinct, valid and at most four; an owner without one is delivered
// without a mail, and nothing is sent; one bad address among them is left out, not the mail.
func TestTheRecipientsAreTheOwnersAddressesBounded(t *testing.T) {
	f := startFake(t, &fakeSMTP{})
	c := config(f)
	var asked []r.Identity
	c.Recipients = func(i r.Identity) []string {
		asked = append(asked, i)
		switch i.Subject {
		case "many":
			return []string{"a@example.com", "b@example.com", "a@example.com", "not an address", "c@example.com", "d@example.com", "e@example.com"}
		case "none":
			return nil
		}
		return []string{testTo}
	}
	m, e := email.New(c)
	if e != nil {
		t.Fatal(e)
	}
	n := notification()
	n.Subject = "none"
	if res := m.Deliver(context.Background(), n); res.Outcome != r.DeliveryOutcomeDelivered || res.Status != 0 {
		t.Fatalf("an owner without an address: %+v", res)
	}
	if got, _ := f.taken(); len(got) != 0 {
		t.Fatal("a mail was sent to no one")
	}
	n.Subject = "many"
	if res := m.Deliver(context.Background(), n); res.Outcome != r.DeliveryOutcomeDelivered {
		t.Fatalf("%+v", res)
	}
	got, _ := f.taken()
	if len(got) != 1 || strings.Join(got[0].To, " ") != "a@example.com b@example.com c@example.com d@example.com" {
		t.Fatalf("%+v", got)
	}
	if h, _ := parsed(t, got[0]); h.Get("To") != "a@example.com, b@example.com, c@example.com, d@example.com" {
		t.Fatalf("%v", h)
	}
	if len(asked) != 2 || asked[0] != (r.Identity{Tenant: n.Tenant, Issuer: n.Issuer, Subject: "none"}) || asked[1].Subject != "many" {
		t.Fatalf("%+v", asked)
	}
}

// STARTTLS is required: a server that offers it is spoken to over TLS from then on, with the credentials; one that does not offer it gets
// nothing (not even MAIL), and the attempt is unreachable. The password goes over TLS, or over plain loopback where smtp allows it, and a wrong
// one is rejected and not tried again.
func TestSTARTTLSIsRequiredAndTheCredentialsGoOverIt(t *testing.T) {
	serverCfg, clientCfg := serverTLS(t)
	f := startFake(t, &fakeSMTP{tls: serverCfg, users: map[string]string{testUser: testPassword}})
	c := config(f)
	c.StartTLS, c.LoopbackPlain, c.ConsoleURL, c.TLS, c.Username, c.Password = true, false, "https://reporting.example.com", clientCfg, testUser, testPassword
	m, e := email.New(c)
	if e != nil {
		t.Fatal(e)
	}
	if res := m.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeDelivered {
		t.Fatalf("%+v", res)
	}
	got, clear := f.taken()
	if len(got) != 1 || !got[0].TLS || got[0].User != testUser || clear != 0 {
		t.Fatalf("%+v, %d MAIL in the clear", got, clear)
	}
	// The certificate is checked: a client that trusts no one is unreachable, and nothing was sent.
	c.TLS = nil
	m, _ = email.New(c)
	if res := m.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeUnreachable {
		t.Fatalf("an untrusted certificate: %+v", res)
	}
	// A wrong password is a permanent refusal.
	c.TLS, c.Password = clientCfg, "wrong-"+testPassword
	m, _ = email.New(c)
	if res := m.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeRejected || res.Status != 535 {
		t.Fatalf("a wrong password: %+v", res)
	}
	if got, clear := f.taken(); len(got) != 1 || clear != 0 {
		t.Fatalf("%d messages, %d MAIL in the clear", len(got), clear)
	}
	// A server without STARTTLS, while it is required: nothing is sent in the clear.
	plain := startFake(t, &fakeSMTP{users: map[string]string{testUser: testPassword}})
	c = config(plain)
	c.StartTLS, c.LoopbackPlain, c.ConsoleURL, c.Username, c.Password = true, false, "https://reporting.example.com", testUser, testPassword
	m, _ = email.New(c)
	if res := m.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeUnreachable {
		t.Fatalf("no STARTTLS offered: %+v", res)
	}
	if got, _ := plain.taken(); len(got) != 0 {
		t.Fatal("a mail went in the clear to a server that offers no TLS")
	}
	// Plain loopback with the option, and the credentials: smtp.PlainAuth allows loopback, and the mail is taken.
	c = config(plain)
	c.Username, c.Password = testUser, testPassword
	m, _ = email.New(c)
	if res := m.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeDelivered {
		t.Fatalf("plain loopback with credentials: %+v", res)
	}
	if got, _ := plain.taken(); len(got) != 1 || got[0].User != testUser || got[0].TLS {
		t.Fatalf("%+v", got)
	}
}

// Every reply has its outcome word: 4yz throttled (tried again), 5yz rejected (not), no answer unreachable, within the timeout.
func TestOutcomesOfReplies(t *testing.T) {
	refusing := startFake(t, &fakeSMTP{rejectRcpt: testTo})
	m, _ := email.New(config(refusing))
	if res := m.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeRejected || res.Status != 550 {
		t.Fatalf("a refused recipient: %+v", res)
	}
	busy := startFake(t, &fakeSMTP{dataReply: "451 try again later"})
	m, _ = email.New(config(busy))
	if res := m.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeThrottled || res.Status != 451 || !res.Retry() {
		t.Fatalf("a busy server: %+v", res)
	}
	if got, _ := busy.taken(); len(got) != 0 {
		t.Fatal("a refused message was recorded")
	}
	broken := startFake(t, &fakeSMTP{dataReply: "554 transaction failed"})
	m, _ = email.New(config(broken))
	if res := m.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeRejected || res.Status != 554 || res.Retry() {
		t.Fatalf("a failed transaction: %+v", res)
	}
	silent := startFake(t, &fakeSMTP{silent: true})
	c := config(silent)
	c.Timeout = time.Second
	m, _ = email.New(c)
	start := time.Now()
	if res := m.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeUnreachable || res.Status != 0 || time.Since(start) > 2500*time.Millisecond {
		t.Fatalf("%+v after %v", res, time.Since(start))
	}
	// A context that ends ends the session too.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	c.Timeout = 10 * time.Second
	m, _ = email.New(c)
	start = time.Now()
	if res := m.Deliver(ctx, notification()); res.Outcome != r.DeliveryOutcomeUnreachable || time.Since(start) > 2*time.Second {
		t.Fatalf("%+v after %v", res, time.Since(start))
	}
	closed := startFake(t, &fakeSMTP{})
	c = config(closed)
	_ = closed.ln.Close()
	m, _ = email.New(c)
	if res := m.Deliver(context.Background(), notification()); res.Outcome != r.DeliveryOutcomeUnreachable {
		t.Fatalf("a closed port: %+v", res)
	}
}
