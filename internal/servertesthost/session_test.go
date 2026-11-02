package main

import (
	"bufio"
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The real server as a separate process: built, configured with files, started, probed, reloaded with SIGHUP and drained with SIGTERM, with the
// exit codes. Everything runs against the memory store and the contract simulator that this host registers; nothing needs a database. The
// transcript of the session is written to $REPORTING_TRANSCRIPT when it is set.

const (
	issuer   = "https://issuer.example.test"
	tenant   = "tenant-host-Zq7"
	subject  = "subject-host-Zq7"
	clientID = "client-host-Zq7"
)

type transcript struct{ b bytes.Buffer }

func (t *transcript) say(format string, args ...any) { fmt.Fprintf(&t.b, format+"\n", args...) }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func goTool() string { return filepath.Join(runtime.GOROOT(), "bin", "go") }

func build(t *testing.T, dir, pkg, name string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	cmd := exec.Command(goTool(), "build", "-o", out, pkg)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, b)
	}
	return out
}

func jwk(kid string, k *rsa.PrivateKey) map[string]string {
	return map[string]string{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256", "n": b64(k.N.Bytes()), "e": "AQAB"}
}

func token(t *testing.T, kid string, k *rsa.PrivateKey) string {
	now := time.Now().Unix()
	payload, _ := json.Marshal(map[string]any{"iss": issuer, "sub": subject, "aud": "reporting-api", "exp": now + 300, "iat": now - 5, "jti": "j-" + kid, "client_id": clientID, "scope": "reporting.read"})
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "at+jwt", "kid": kid})
	signing := b64(header) + "." + b64(payload)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + b64(sig)
}

func write(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestTheServerAsASeparateProcess(t *testing.T) {
	tr := &transcript{}
	dir := t.TempDir()
	host := build(t, dir, ".", "reporting-servertesthost")
	shipped := build(t, dir, "idp.local/reporting/cmd/reporting-server", "reporting-server")
	run := func(bin string, env []string, args ...string) (string, string, int) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), env...)
		var out, errs bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errs
		err := cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return out.String(), errs.String(), code
	}

	// The files of a deployment.
	key1, _ := rsa.GenerateKey(rand.Reader, 2048)
	key2, _ := rsa.GenerateKey(rand.Reader, 2048)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "host"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, BasicConstraintsValid: true, IsCA: true}
	der, _ := x509.CreateCertificate(rand.Reader, tpl, tpl, &ec.PublicKey, ec)
	keyDER, _ := x509.MarshalECPrivateKey(ec)
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	cert, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	write(t, cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	write(t, keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	jwks, bindings, cursor := filepath.Join(dir, "jwks.json"), filepath.Join(dir, "bindings.json"), filepath.Join(dir, "cursor.key")
	setJWKS := func(keys ...map[string]string) {
		b, _ := json.Marshal(map[string]any{"keys": keys})
		tmp := jwks + ".new"
		write(t, tmp, b, 0o644)
		if err := os.Rename(tmp, jwks); err != nil {
			t.Fatal(err)
		}
	}
	setJWKS(jwk("kid-one", key1))
	write(t, bindings, []byte(fmt.Sprintf(`[{"subject":%q,"client_id":%q,"tenant":%q,"integrations":["brant-contract-demo"]}]`, subject, clientID, tenant)), 0o644)
	const cursorKey = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	write(t, cursor, []byte(cursorKey), 0o600)
	config := filepath.Join(dir, "config.json")
	doc := fmt.Sprintf(`{"listen":"127.0.0.1:0","tls_cert_file":%q,"tls_key_file":%q,"allow_simulator":true,"workers":1,"shutdown_grace":10,"profile_label":"process-test",
"issuer":{"issuer":%q,"audience":"reporting-api","required_scope":"reporting.read","max_token_lifetime":900,"jwks_file":%q,"bindings_file":%q},
"cursor_key_files":[%q],
"connections":[{"tenant":%q,"integration":"brant-contract-demo","source_tenant":"tenant_a","adapter":"contract-simulator","options":{"tenant":%q,"issuer":%q,"subject":%q}}]}`,
		cert, keyFile, issuer, jwks, bindings, cursor, tenant, tenant, issuer, subject)
	write(t, config, []byte(doc), 0o644)
	tr.say("$ cat config.json   (no secret in it: files and the environment carry them)\n%s\n", doc)

	// The shipped command registers no adapter.
	_, errs, code := run(shipped, []string{"REPORTING_STORE_DSN=postgresql://app:hostpw-Zq7@db.example/reporting?sslmode=verify-full"}, "run", "-config", config)
	tr.say("$ REPORTING_STORE_DSN=... reporting-server run -config config.json   # the shipped command\nexit %d\n%s", code, errs)
	if code != 1 || !strings.Contains(errs, `no adapter is registered for integration "brant-contract-demo"`) || strings.Contains(errs, "hostpw-Zq7") {
		t.Fatalf("the shipped command: %d %s", code, errs)
	}
	out, errs, code := run(shipped, nil, "version")
	tr.say("$ reporting-server version\n%sexit %d", out, code)
	if code != 0 || !strings.Contains(out, "schema 9") {
		t.Fatalf("version: %d %s %s", code, out, errs)
	}

	// check-config on the host that registers the simulator.
	out, errs, code = run(host, []string{"REPORTING_STORE_DSN=postgresql://app:hostpw-Zq7@db.example/reporting?sslmode=verify-full"}, "check-config", "-config", config)
	tr.say("$ servertesthost check-config -config config.json\n%sexit %d %s", out, code, errs)
	if code != 0 || !strings.Contains(out, "configuration: ok") || strings.Contains(out+errs, "hostpw-Zq7") || strings.Contains(out+errs, cursorKey) {
		t.Fatalf("check-config: %d %s %s", code, out, errs)
	}
	write(t, cursor, []byte(cursorKey), 0o644)
	_, errs, code = run(host, nil, "check-config", "-config", config)
	tr.say("$ chmod 644 cursor.key; servertesthost check-config -config config.json\nexit %d %s", code, errs)
	if code != 1 || !strings.Contains(errs, "cursor_key_files[0]: the file's mode must be 0600 or 0400") {
		t.Fatalf("check-config with a readable key: %d %s", code, errs)
	}
	write(t, cursor, []byte(cursorKey), 0o600)

	// run: a separate process.
	cmd := exec.Command(host, "run", "-config", config)
	cmd.Env = os.Environ()
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan map[string]any, 256)
	var all bytes.Buffer
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			all.WriteString(sc.Text() + "\n")
			m := map[string]any{}
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				lines <- m
			}
		}
		close(lines)
	}()
	waitFor := func(event string, match func(map[string]any) bool) map[string]any {
		t.Helper()
		deadline := time.After(30 * time.Second)
		for {
			select {
			case m, ok := <-lines:
				if !ok {
					t.Fatalf("the process ended before %q", event)
				}
				if m["event"] == event && (match == nil || match(m)) {
					return m
				}
			case <-deadline:
				t.Fatalf("no %q line", event)
			}
		}
	}
	start := waitFor("start", nil)
	addr := fmt.Sprintf("127.0.0.1:%d", int(start["listen_port"].(float64)))
	tr.say("$ servertesthost run -config config.json &   # pid %d\n%s", cmd.Process.Pid, mustLine(start))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: 10 * time.Second}
	get := func(path, tok string) (int, string) {
		req, _ := http.NewRequest("GET", "https://"+addr+path, nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		status, body := get(path, "")
		tr.say("$ curl https://%s%s\n%d %s", addr, path, status, body)
		if status != 200 {
			t.Fatalf("%s: %d", path, status)
		}
	}
	one, two := token(t, "kid-one", key1), token(t, "kid-two", key2)
	status, body := get("/api/reporting/v1/session", one)
	tr.say("$ curl -H 'Authorization: Bearer <token signed by kid-one>' .../api/reporting/v1/session\n%d %s", status, body)
	if status != 200 {
		t.Fatalf("%d %s", status, body)
	}
	status, body = get("/api/reporting/v1/session", two)
	tr.say("$ the same with a token signed by kid-two (not yet trusted)\n%d %s", status, body)
	if status != 401 {
		t.Fatalf("%d", status)
	}
	status, body = get("/api/reporting/v1/capabilities", one)
	tr.say("$ curl .../api/reporting/v1/capabilities\n%d %s", status, body)
	if status != 200 || !strings.Contains(body, `"production_approved":false`) || !strings.Contains(body, `"profile":"simulated:process-test"`) {
		t.Fatalf("%d %s", status, body)
	}

	// SIGHUP: the overlap of a rotation, with no restart.
	setJWKS(jwk("kid-one", key1), jwk("kid-two", key2))
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	reload := waitFor("reload", nil)
	tr.say("$ cp jwks-with-both-keys.json jwks.json; kill -HUP %d\n%s", cmd.Process.Pid, mustLine(reload))
	if reload["outcome"] != "ok" {
		t.Fatalf("%v", reload)
	}
	if status, _ = get("/api/reporting/v1/session", two); status != 200 {
		t.Fatalf("after the reload: %d", status)
	}
	tr.say("the token signed by kid-two: 200 after the reload")
	// A broken file: the old configuration stays.
	write(t, jwks+".new", []byte("{"), 0o644)
	if err := os.Rename(jwks+".new", jwks); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Process.Signal(syscall.SIGHUP)
	refused := waitFor("reload", func(m map[string]any) bool { return m["outcome"] == "refused" })
	tr.say("$ echo '{' > jwks.json; kill -HUP %d\n%s", cmd.Process.Pid, mustLine(refused))
	if status, _ = get("/api/reporting/v1/session", one); status != 200 {
		t.Fatalf("the previous configuration is not in force: %d", status)
	}
	tr.say("the token signed by kid-one: still 200")

	// SIGTERM: the drain, and the exit code.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	begin := waitFor("drain_begin", nil)
	end := waitFor("drain_end", nil)
	state := make(chan error, 1)
	go func() { state <- cmd.Wait() }()
	if err := <-state; err != nil {
		t.Fatalf("exit: %v", err)
	}
	tr.say("$ kill -TERM %d\n%s\n%s\nexit code %d", cmd.Process.Pid, mustLine(begin), mustLine(end), cmd.ProcessState.ExitCode())
	if cmd.ProcessState.ExitCode() != 0 || end["clean"] != true {
		t.Fatalf("exit %d, %v", cmd.ProcessState.ExitCode(), end)
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Error("the port still accepts")
	}
	for range lines {
	}
	for _, secret := range []string{one, two, subject, tenant, clientID, cursorKey, "hostpw-Zq7"} {
		if strings.Contains(all.String(), secret) {
			t.Errorf("the process logged %q", secret)
		}
	}
	if p := os.Getenv("REPORTING_TRANSCRIPT"); p != "" {
		if err := os.WriteFile(p, tr.b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func mustLine(m map[string]any) string { b, _ := json.Marshal(m); return string(b) }
