package objects

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	r "idp.local/reporting/reporting"
)

const (
	testAccess = "AKIATESTACCESSKEY0001"
	testSecret = "s3cr3t/KEY+with=odd/chars0123456789ABCD"
	testBucket = "unit-bucket"
)

func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func client(t *testing.T, endpoint string) *S3 {
	t.Helper()
	c, err := NewS3(S3{Endpoint: endpoint, Bucket: testBucket, Region: "us-east-1", AccessKey: testAccess, SecretKey: testSecret, LoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// verifier is a server that accepts a request only if its signature is the one the specification's algorithm (derive, qualified above against the
// published examples) gives for the request as the server received it: the method, the Host header, the decoded path, the decoded query and the
// signed headers it names. A client that signs one thing and sends another fails here.
type verifier struct {
	mu       sync.Mutex
	requests []seen
	handle   func(w http.ResponseWriter, req *http.Request, body []byte)
}
type seen struct {
	method, path, rawQuery, payloadHash string
	body                                []byte
}

var authHeader = regexp.MustCompile(`^AWS4-HMAC-SHA256 Credential=([^/]+)/(\d{8})/([a-z0-9-]+)/s3/aws4_request, SignedHeaders=([a-z0-9;-]+), Signature=([0-9a-f]{64})$`)

func (v *verifier) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	m := authHeader.FindStringSubmatch(req.Header.Get("Authorization"))
	if m == nil || m[1] != testAccess {
		w.WriteHeader(403)
		return
	}
	at, err := time.Parse(dateLayout, req.Header.Get("x-amz-date"))
	if err != nil || m[2] != at.Format("20060102") {
		w.WriteHeader(403)
		return
	}
	headers := map[string]string{}
	for _, name := range strings.Split(m[4], ";") {
		if name != "host" {
			headers[name] = req.Header.Get(name)
		}
	}
	var query [][2]string
	for k, vs := range req.URL.Query() {
		for _, value := range vs {
			query = append(query, [2]string{k, value})
		}
	}
	creds := credentials{access: testAccess, secret: testSecret, region: m[3]}
	d := derive(creds, req.Method, req.Host, req.URL.Path, query, headers, req.Header.Get("x-amz-content-sha256"), at)
	if d.Signature != m[5] || d.SignedHeaders != m[4] || m[4] != "host;x-amz-content-sha256;x-amz-date" {
		w.WriteHeader(403)
		return
	}
	// The payload hash is the body's, on every request (x-amz-content-sha256 is never UNSIGNED-PAYLOAD).
	if digest(body) != req.Header.Get("x-amz-content-sha256") {
		w.WriteHeader(400)
		return
	}
	v.mu.Lock()
	v.requests = append(v.requests, seen{req.Method, req.URL.EscapedPath(), req.URL.RawQuery, req.Header.Get("x-amz-content-sha256"), body})
	v.mu.Unlock()
	v.handle(w, req, body)
}

func (v *verifier) all() []seen {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]seen(nil), v.requests...)
}

func serve(t *testing.T, handle func(w http.ResponseWriter, req *http.Request, body []byte)) (*S3, *verifier) {
	t.Helper()
	v := &verifier{handle: handle}
	server := httptest.NewServer(v)
	t.Cleanup(server.Close)
	return client(t, server.URL), v
}

func TestRequestsAreSignedAsSentAndAddressTheBucketPathStyle(t *testing.T) {
	content := []byte("%PDF-1.7 hello")
	sum := digest(content)
	c, v := serve(t, func(w http.ResponseWriter, req *http.Request, body []byte) {
		switch req.Method {
		case "HEAD":
		case "PUT":
		case "GET":
			if strings.HasSuffix(req.URL.Path, "/artifacts/x y") || strings.Contains(req.URL.Path, "/artifacts/") {
				w.Write(content)
				return
			}
			io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
		case "DELETE":
			w.WriteHeader(204)
		}
	})
	ctx := context.Background()
	if err := c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	key := "artifacts/x y$é~"
	if err := c.Put(ctx, key, content, sum); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Get(ctx, key, sum, len(content)); err != nil || string(got) != string(content) {
		t.Fatal(got, err)
	}
	if _, _, err := c.List(ctx, "artifacts/", "tok/en+1=", 7); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	got := v.all()
	want := []seen{
		{"HEAD", "/unit-bucket", "", emptySHA256, nil},
		{"PUT", "/unit-bucket/artifacts/x%20y%24%C3%A9~", "", sum, content},
		{"GET", "/unit-bucket/artifacts/x%20y%24%C3%A9~", "", emptySHA256, nil},
		{"GET", "/unit-bucket", "continuation-token=tok%2Fen%2B1%3D&list-type=2&max-keys=7&prefix=artifacts%2F", emptySHA256, nil},
		{"DELETE", "/unit-bucket/artifacts/x%20y%24%C3%A9~", "", emptySHA256, nil},
	}
	if len(got) != len(want) {
		t.Fatalf("%d requests, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].method != want[i].method || got[i].path != want[i].path || got[i].rawQuery != want[i].rawQuery || got[i].payloadHash != want[i].payloadHash || string(got[i].body) != string(want[i].body) {
			t.Errorf("request %d is %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestEncodingOfKeysAndQueryStrings(t *testing.T) {
	for in, want := range map[string]string{
		"/b/artifacts/0123abcd":     "/b/artifacts/0123abcd",
		"/b/a b/c":                  "/b/a%20b/c",
		"/b/ü":                      "/b/%C3%BC",
		"/b/~-_.":                   "/b/~-_.",
		"/b/a+b=c&d?e#f%g":          "/b/a%2Bb%3Dc%26d%3Fe%23f%25g",
		"/b/$!*'();:@,":             "/b/%24%21%2A%27%28%29%3B%3A%40%2C",
		"/":                         "/",
		"/b/artifacts/a/../b":       "/b/artifacts/a/../b",
		"/b/中文":                     "/b/%E4%B8%AD%E6%96%87",
		"/b/artifacts/\x01\x7f\xff": "/b/artifacts/%01%7F%FF",
	} {
		if got := encodePath(in); got != want {
			t.Errorf("encodePath(%q) = %q, want %q", in, got, want)
		}
	}
	got := encodeQuery([][2]string{{"prefix", "artifacts/"}, {"a", "2"}, {"a", "1"}, {"b c", "d+e"}, {"continuation-token", "1/2+3=="}, {"empty", ""}})
	want := "a=1&a=2&b%20c=d%2Be&continuation-token=1%2F2%2B3%3D%3D&empty=&prefix=artifacts%2F"
	if got != want {
		t.Errorf("encodeQuery = %q, want %q", got, want)
	}
	if got := trimValue("  a   b \t c  "); got != "a b c" {
		t.Errorf("header values are trimmed and their runs of spaces made one: %q", got)
	}
}

func TestConstructionRefusals(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	good := S3{Endpoint: "https://s3.example.com", Bucket: "reports-bucket", Region: "eu-west-1", AccessKey: "AK", SecretKey: "SK"}
	if _, err := NewS3(good); err != nil {
		t.Fatalf("a valid configuration is refused: %v", err)
	}
	if _, err := NewS3(S3{Endpoint: "http://127.0.0.1:9000/", Bucket: "reports-bucket", Region: "us-east-1", AccessKey: "AK", SecretKey: "SK", LoopbackHTTP: true}); err != nil {
		t.Fatalf("the loopback http endpoint with its option is refused: %v", err)
	}
	if _, err := NewS3(S3{Endpoint: "http://[::1]:9000", Bucket: "reports-bucket", Region: "us-east-1", AccessKey: "AK", SecretKey: "SK", LoopbackHTTP: true}); err != nil {
		t.Fatalf("the IPv6 loopback endpoint with its option is refused: %v", err)
	}
	for name, mutate := range map[string]func(*S3){
		"http on a remote host":               func(c *S3) { c.Endpoint = "http://s3.example.com"; c.LoopbackHTTP = true },
		"http on a remote IP":                 func(c *S3) { c.Endpoint = "http://10.0.0.5:9000"; c.LoopbackHTTP = true },
		"http on localhost by name":           func(c *S3) { c.Endpoint = "http://localhost:9000"; c.LoopbackHTTP = true },
		"http on loopback without the option": func(c *S3) { c.Endpoint = "http://127.0.0.1:9000" },
		"https with the loopback option":      func(c *S3) { c.LoopbackHTTP = true },
		"http with a CA":                      func(c *S3) { c.Endpoint = "http://127.0.0.1:9000"; c.LoopbackHTTP = true; c.CA = ca },
		"another scheme":                      func(c *S3) { c.Endpoint = "ftp://s3.example.com" },
		"no scheme":                           func(c *S3) { c.Endpoint = "s3.example.com" },
		"empty endpoint":                      func(c *S3) { c.Endpoint = "" },
		"a path":                              func(c *S3) { c.Endpoint = "https://s3.example.com/bucket" },
		"a query":                             func(c *S3) { c.Endpoint = "https://s3.example.com?x=1" },
		"a fragment":                          func(c *S3) { c.Endpoint = "https://s3.example.com#x" },
		"user information":                    func(c *S3) { c.Endpoint = "https://user:pass@s3.example.com" },
		"a bad port":                          func(c *S3) { c.Endpoint = "https://s3.example.com:99999" },
		"no host":                             func(c *S3) { c.Endpoint = "https://" },
		"an unreadable CA file":               func(c *S3) { c.CA = filepath.Join(t.TempDir(), "missing.pem") },
		"a CA file with no certificate":       func(c *S3) { c.CA = ca },
		"no bucket":                           func(c *S3) { c.Bucket = "" },
		"an upper-case bucket":                func(c *S3) { c.Bucket = "Reports" },
		"a short bucket":                      func(c *S3) { c.Bucket = "ab" },
		"a bucket with a slash":               func(c *S3) { c.Bucket = "a/b/c" },
		"a bucket that is an IP":              func(c *S3) { c.Bucket = "192.168.5.4" },
		"a bucket ending in a hyphen":         func(c *S3) { c.Bucket = "reports-" },
		"no region":                           func(c *S3) { c.Region = "" },
		"a region with a space":               func(c *S3) { c.Region = "us east" },
		"no access key":                       func(c *S3) { c.AccessKey = "" },
		"no secret key":                       func(c *S3) { c.SecretKey = "" },
		"a secret with a space":               func(c *S3) { c.SecretKey = "a b" },
		"a secret with a newline":             func(c *S3) { c.SecretKey = "ab\n" },
	} {
		c := good
		mutate(&c)
		_, err := NewS3(c)
		if !errors.Is(err, r.ErrInvalid) {
			t.Errorf("%s: accepted (%v)", name, err)
			continue
		}
		// The refusal says what is wrong with the configuration and repeats none of its secrets.
		if strings.Contains(err.Error(), c.SecretKey) && c.SecretKey != "" {
			t.Errorf("%s: the refusal repeats the secret: %v", name, err)
		}
	}
	// A client that was not made by NewS3 refuses everything.
	var zero S3
	if err := zero.Check(context.Background()); err == nil {
		t.Error("the zero value made a request")
	}
	if _, err := (&S3{Endpoint: "https://s3.example.com"}).Get(context.Background(), "artifacts/"+strings.Repeat("a", 32), digest([]byte("x")), 1); err == nil {
		t.Error("a client made without NewS3 made a request")
	}
}

func TestPutRefusesWhatItCannotSendSafely(t *testing.T) {
	c, v := serve(t, func(w http.ResponseWriter, req *http.Request, body []byte) {})
	ctx := context.Background()
	content := []byte("abc")
	sum := digest(content)
	for name, call := range map[string]func() error{
		"a digest that is not the content's": func() error { return c.Put(ctx, "artifacts/a", content, digest([]byte("other"))) },
		"a digest that is not hex":           func() error { return c.Put(ctx, "artifacts/a", content, strings.ToUpper(sum)) },
		"no content":                         func() error { return c.Put(ctx, "artifacts/a", nil, digest(nil)) },
		"too much content":                   func() error { b := make([]byte, r.MaxArtifactBytes+1); return c.Put(ctx, "artifacts/a", b, digest(b)) },
		"an empty key":                       func() error { return c.Put(ctx, "", content, sum) },
		"a dot segment":                      func() error { return c.Put(ctx, "artifacts/../x", content, sum) },
		"an empty segment":                   func() error { return c.Put(ctx, "artifacts//x", content, sum) },
		"a leading slash":                    func() error { return c.Put(ctx, "/artifacts/x", content, sum) },
		"a control character":                func() error { return c.Put(ctx, "artifacts/a\nb", content, sum) },
	} {
		if err := call(); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := len(v.all()); n != 0 {
		t.Errorf("%d requests were sent for calls that were refused", n)
	}
}

func TestGetChecksTheObjectAgainstTheRowBeforeReturningAByte(t *testing.T) {
	content := []byte("0123456789")
	sum := digest(content)
	ctx := context.Background()
	serveBody := func(status int, body []byte, headers map[string]string) *S3 {
		c, _ := serve(t, func(w http.ResponseWriter, req *http.Request, _ []byte) {
			for k, v := range headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(status)
			w.Write(body)
		})
		return c
	}
	// A body sent in chunks has no Content-Length to check first: the length of what was read is checked after.
	serveChunked := func(body []byte) *S3 {
		c, _ := serve(t, func(w http.ResponseWriter, req *http.Request, _ []byte) {
			w.Write(body[:1])
			w.(http.Flusher).Flush()
			w.Write(body[1:])
		})
		return c
	}
	// The object as the row describes it is returned.
	if got, err := serveBody(200, content, nil).Get(ctx, "artifacts/a", sum, 10); err != nil || string(got) != string(content) {
		t.Fatal(got, err)
	}
	if got, err := serveChunked(content).Get(ctx, "artifacts/a", sum, 10); err != nil || string(got) != string(content) {
		t.Fatal("a chunked body", got, err)
	}
	for name, c := range map[string]struct {
		store  *S3
		sum    string
		length int
		reason string
	}{
		"a body of another digest":       {serveBody(200, []byte("0123456780"), nil), sum, 10, "digest"},
		"a shorter body":                 {serveBody(200, content[:9], nil), sum, 10, "length"},
		"a longer body":                  {serveBody(200, append(content, 'x'), nil), sum, 10, "length"},
		"a row that says another length": {serveBody(200, content, nil), sum, 9, "length"},
		"a missing object":               {serveBody(404, []byte("<Error><Code>NoSuchKey</Code><Key>artifacts/a</Key></Error>"), nil), sum, 10, "missing"},
		"a row whose digest is another":  {serveBody(200, content, map[string]string{"Content-Length": "10"}), digest([]byte("x")), 10, "digest"},
		"a header of another length":     {serveBody(200, content, map[string]string{"Content-Length": "11"}), sum, 10, "length"},
		"a shorter chunked body":         {serveChunked(content[:9]), sum, 10, "length"},
		"a longer chunked body":          {serveChunked(append(append([]byte(nil), content...), 'x')), sum, 10, "length"},
	} {
		got, err := c.store.Get(ctx, "artifacts/a", c.sum, c.length)
		if !errors.Is(err, r.ErrIntegrity) || got != nil {
			t.Errorf("%s: %v %q", name, err, got)
		}
		// The reason is the row's length, or its digest, or a missing object; never the object's key or body.
		if err != nil && !strings.Contains(err.Error(), c.reason) {
			t.Errorf("%s: the error says %q, want it to say %q", name, err, c.reason)
		}
	}
	// A row that cannot be right is refused without a request.
	c, v := serve(t, func(w http.ResponseWriter, req *http.Request, _ []byte) { w.Write(content) })
	for name, call := range map[string]func() error{
		"length 0":                 func() error { _, err := c.Get(ctx, "artifacts/a", sum, 0); return err },
		"length over the bound":    func() error { _, err := c.Get(ctx, "artifacts/a", sum, r.MaxArtifactBytes+1); return err },
		"a digest that is not hex": func() error { _, err := c.Get(ctx, "artifacts/a", "zz", 10); return err },
	} {
		if err := call(); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(v.all()) != 0 {
		t.Error("a request was sent for a row that cannot be right")
	}
}

func TestGetReadsAtMostOneByteMoreThanTheRowSays(t *testing.T) {
	// An object far larger than the row says is not read to its end: the client stops one byte past the length.
	var sent atomic.Int64
	c, _ := serve(t, func(w http.ResponseWriter, req *http.Request, _ []byte) {
		chunk := make([]byte, 64<<10)
		for i := 0; i < 400; i++ { // 25 MiB
			n, err := w.Write(chunk)
			sent.Add(int64(n))
			if err != nil {
				return
			}
		}
	})
	if _, err := c.Get(context.Background(), "artifacts/a", digest([]byte("x")), 1000); !errors.Is(err, r.ErrIntegrity) {
		t.Fatal(err)
	}
	if sent.Load() >= 25<<20 {
		t.Errorf("the whole object (%d bytes) was read for a row of 1000", sent.Load())
	}
}

func TestErrorsCarryNoBodyKeyEndpointOrCredential(t *testing.T) {
	const marker = "BODYMARKER-must-not-leak"
	content := []byte("abc")
	sum := digest(content)
	ctx := context.Background()
	for _, status := range []int{400, 401, 403, 409, 500, 502, 503} {
		var endpoint string
		c, _ := serve(t, func(w http.ResponseWriter, req *http.Request, _ []byte) {
			w.WriteHeader(status)
			fmt.Fprintf(w, `<Error><Code>%s</Code><Message>%s</Message><Key>artifacts/secretkey</Key><AWSAccessKeyId>%s</AWSAccessKeyId></Error>`, marker, marker, testAccess)
		})
		endpoint = c.Endpoint
		key := "artifacts/secretkey"
		errs := map[string]error{
			"check":  c.Check(ctx),
			"put":    c.Put(ctx, key, content, sum),
			"delete": c.Delete(ctx, key),
		}
		_, errs["get"] = c.Get(ctx, key, sum, 3)
		_, _, errs["list"] = c.List(ctx, "artifacts/", "", 10)
		host := strings.TrimPrefix(endpoint, "http://")
		for op, err := range errs {
			if err == nil {
				t.Errorf("status %d, %s: no error", status, op)
				continue
			}
			var o *r.ObjectStoreError
			if !errors.As(err, &o) || o.Status != status || !errors.Is(err, r.ErrUnavailable) {
				t.Errorf("status %d, %s: %v is not the store being unavailable with that status", status, op, err)
			}
			for _, secret := range []string{marker, key, "secretkey", host, endpoint, testAccess, testSecret, testBucket, "127.0.0.1"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("status %d, %s: the error %q carries %q", status, op, err, secret)
				}
			}
			if want := fmt.Sprintf("object store unavailable (status %d)", status); err.Error() != want {
				t.Errorf("status %d, %s: the text is %q, want %q", status, op, err, want)
			}
		}
	}
}

func TestNetworkErrorsCarryNothingOfTheTransportAndAreRetriedOnce(t *testing.T) {
	content := []byte("abc")
	sum := digest(content)
	// A server that drops every connection: one try and one retry, then an error that names nothing.
	var tries atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		tries.Add(1)
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	defer server.Close()
	c := client(t, server.URL)
	err := c.Put(context.Background(), "artifacts/a", content, sum)
	var o *r.ObjectStoreError
	if !errors.As(err, &o) || o.Status != 0 || tries.Load() != 2 {
		t.Fatalf("%v after %d tries", err, tries.Load())
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), "EOF") || err.Error() != "object store unavailable" {
		t.Errorf("the error carries the transport's text: %q", err)
	}
	// A server that drops the first connection and then answers: the retry succeeds, with a signature of its own.
	var first atomic.Bool
	var dates []string
	var mu sync.Mutex
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		dates = append(dates, req.Header.Get("Authorization"))
		mu.Unlock()
		if first.CompareAndSwap(false, true) {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.WriteHeader(200)
	}))
	defer server2.Close()
	c2 := client(t, server2.URL)
	if err := c2.Check(context.Background()); err != nil {
		t.Fatal("the retry of an idempotent request after a network error failed:", err)
	}
	if len(dates) != 2 {
		t.Fatalf("%d attempts, want 2", len(dates))
	}
	// An answer is never retried, whatever its status.
	tries.Store(0)
	server3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { tries.Add(1); w.WriteHeader(500) }))
	defer server3.Close()
	if err := client(t, server3.URL).Delete(context.Background(), "artifacts/a"); err == nil || tries.Load() != 1 {
		t.Fatalf("%v after %d tries: an answered request was retried", err, tries.Load())
	}
	// A cancelled context ends the request and is not retried.
	tries.Store(0)
	server4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { tries.Add(1); time.Sleep(200 * time.Millisecond) }))
	defer server4.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := client(t, server4.URL).Check(ctx); err == nil || tries.Load() > 1 {
		t.Fatalf("%v after %d tries", err, tries.Load())
	}
}

func TestEveryRequestHasATimeout(t *testing.T) {
	if RequestTimeout != 10*time.Second {
		t.Fatalf("the per-request timeout is %s, the scope says 10s", RequestTimeout)
	}
	c, err := NewS3(S3{Endpoint: "http://127.0.0.1:9", Bucket: testBucket, Region: "us-east-1", AccessKey: testAccess, SecretKey: testSecret, LoopbackHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.client.Timeout != RequestTimeout {
		t.Errorf("the client's timeout is %s", c.client.Timeout)
	}
	// A hung server ends the request at the timeout (shortened here), in one attempt: a timeout is not retried, a second wait would double it.
	var tries atomic.Int64
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { tries.Add(1); <-release }))
	defer server.Close()
	defer close(release)
	slow := client(t, server.URL)
	slow.client.Timeout = 150 * time.Millisecond
	start := time.Now()
	err = slow.Check(context.Background())
	if err == nil || tries.Load() != 1 || time.Since(start) > 3*time.Second {
		t.Fatalf("%v after %d tries in %s", err, tries.Load(), time.Since(start))
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { followed.Store(true) }))
	defer target.Close()
	c, _ := serve(t, func(w http.ResponseWriter, req *http.Request, _ []byte) {
		http.Redirect(w, req, target.URL+"/elsewhere", http.StatusTemporaryRedirect)
	})
	err := c.Check(context.Background())
	var o *r.ObjectStoreError
	if !errors.As(err, &o) || o.Status != 307 || followed.Load() {
		t.Fatalf("%v, followed=%v", err, followed.Load())
	}
}

func TestListPagesAndRefusesWhatItCannotRead(t *testing.T) {
	ctx := context.Background()
	page := func(body string) *S3 {
		c, _ := serve(t, func(w http.ResponseWriter, req *http.Request, _ []byte) { io.WriteString(w, body) })
		return c
	}
	good := `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>unit-bucket</Name><Prefix>artifacts/</Prefix><KeyCount>2</KeyCount><MaxKeys>2</MaxKeys><IsTruncated>true</IsTruncated>` +
		`<Contents><Key>artifacts/a</Key><LastModified>2026-11-01T10:00:00.123Z</LastModified><ETag>"x"</ETag><Size>3</Size><StorageClass>STANDARD</StorageClass></Contents>` +
		`<Contents><Key>artifacts/b</Key><LastModified>2026-11-01T11:30:00Z</LastModified><Size>3</Size></Contents><NextContinuationToken>tok-2</NextContinuationToken></ListBucketResult>`
	got, next, err := page(good).List(ctx, "artifacts/", "", 2)
	if err != nil || next != "tok-2" || len(got) != 2 || got[0].Key != "artifacts/a" || !got[0].LastModified.Equal(time.Date(2026, 11, 1, 10, 0, 0, 123000000, time.UTC)) || !got[1].LastModified.Equal(time.Date(2026, 11, 1, 11, 30, 0, 0, time.UTC)) {
		t.Fatal(got, next, err)
	}
	last, next, err := page(`<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>artifacts/c</Key><LastModified>2026-11-01T12:00:00.000Z</LastModified></Contents></ListBucketResult>`).List(ctx, "artifacts/", "tok-2", 2)
	if err != nil || next != "" || len(last) != 1 {
		t.Fatal(last, next, err)
	}
	if empty, next, err := page(`<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`).List(ctx, "artifacts/", "", 5); err != nil || next != "" || len(empty) != 0 {
		t.Fatal(empty, next, err)
	}
	for name, body := range map[string]string{
		"not XML":                   "this is not xml",
		"another document":          `<Error><Code>X</Code></Error>`,
		"a key outside the prefix":  `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>other/a</Key><LastModified>2026-11-01T12:00:00Z</LastModified></Contents></ListBucketResult>`,
		"a time it cannot read":     `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>artifacts/a</Key><LastModified>yesterday</LastModified></Contents></ListBucketResult>`,
		"truncated with no token":   `<ListBucketResult><IsTruncated>true</IsTruncated><Contents><Key>artifacts/a</Key><LastModified>2026-11-01T12:00:00Z</LastModified></Contents></ListBucketResult>`,
		"more keys than were asked": `<ListBucketResult><IsTruncated>false</IsTruncated>` + strings.Repeat(`<Contents><Key>artifacts/a</Key><LastModified>2026-11-01T12:00:00Z</LastModified></Contents>`, 3) + `</ListBucketResult>`,
	} {
		_, _, err := page(body).List(ctx, "artifacts/", "", 2)
		var o *r.ObjectStoreError
		if !errors.As(err, &o) {
			t.Errorf("%s: %v", name, err)
		} else if strings.Contains(err.Error(), "artifacts") || strings.Contains(err.Error(), "xml") {
			t.Errorf("%s: the error repeats the listing: %q", name, err)
		}
	}
	for _, limit := range []int{0, -1, MaxListLimit + 1} {
		if _, _, err := page(good).List(ctx, "artifacts/", "", limit); !errors.Is(err, r.ErrInvalid) {
			t.Errorf("limit %d: %v", limit, err)
		}
	}
}

func TestDeleteOfAnObjectThatIsNotThereIsNotAnError(t *testing.T) {
	for _, status := range []int{200, 204, 404} {
		c, _ := serve(t, func(w http.ResponseWriter, req *http.Request, _ []byte) { w.WriteHeader(status) })
		if err := c.Delete(context.Background(), "artifacts/a"); err != nil {
			t.Errorf("status %d: %v", status, err)
		}
	}
}

func TestHTTPSIsVerifiedAgainstTheNamedCAAlone(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(200) }))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	server.Config.ErrorLog = log.New(io.Discard, "", 0) // the handshake the system's roots refuse is the point of the test, not news
	server.StartTLS()
	defer server.Close()
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := S3{Endpoint: server.URL, Bucket: testBucket, Region: "us-east-1", AccessKey: testAccess, SecretKey: testSecret}
	// The system's roots do not know this server: the request fails, and the error says nothing of the certificate or the host.
	plain, err := NewS3(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.Check(context.Background()); err == nil || err.Error() != "object store unavailable" {
		t.Fatalf("a server no root vouches for was trusted: %v", err)
	}
	cfg.CA = caFile
	named, err := NewS3(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := named.Check(context.Background()); err != nil {
		t.Fatalf("the server the named CA vouches for was refused: %v", err)
	}
	// The https endpoint is signed with its Host (the port included) like any other.
	if named.host != strings.TrimPrefix(server.URL, "https://") {
		t.Errorf("host %q", named.host)
	}
}

func TestTheValueNeverPrintsItsCredentials(t *testing.T) {
	c := client(t, "http://127.0.0.1:9")
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%s", c), fmt.Sprint(*c), fmt.Sprintf("%+v", *c), fmt.Sprintf("%#v", *c)} {
		if strings.Contains(s, testSecret) || strings.Contains(s, testAccess) {
			t.Errorf("printing the client prints a credential: %s", s)
		}
	}
}

func TestCreateBucketIsForTestsAlone(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var users []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		code := regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(string(b), "")
		if strings.Contains(code, "CreateBucketForTests") && !strings.HasSuffix(path, filepath.Join("storage", "objects", "testsupport.go")) {
			users = append(users, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 0 {
		t.Errorf("the platform creates buckets in %v: only tests may (ADR 0007 decision 6)", users)
	}
	// Nor does any other bucket-level write exist in the client: its requests are HEAD and GET on the bucket, PUT, GET and DELETE on keys.
	src, err := os.ReadFile("s3.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(src), "http.MethodPut") != 1 {
		t.Error("the client has a PUT other than the object's")
	}
}
