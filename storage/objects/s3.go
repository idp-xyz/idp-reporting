// Package objects holds the object stores artifact content lives in (S04-E, docs/ADR_0007_OBJECT_STORAGE.md): S3, a client for any service that
// speaks the S3 API, written over net/http and the standard library's crypto because an SDK's dependency tree is not worth five operations, and
// Memory, a map with the same contract for the tests that do not need a server. Both satisfy reporting.ObjectStore.
package objects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	r "idp.local/reporting/reporting"
)

// RequestTimeout bounds each request, a retry included.
const RequestTimeout = 10 * time.Second

// maxListBody bounds a listing's XML: a page of 1000 keys is a few hundred KiB.
const maxListBody = 8 << 20

// MaxListLimit is the most keys one List page asks for (the S3 maximum).
const MaxListLimit = 1000

// S3 is an S3-compatible object store: path-style addressing, one bucket, Signature Version 4. Fill the fields and call NewS3; the zero value
// and a copy made without NewS3 refuse every call.
//
//   - Endpoint is the service as scheme://host[:port] and nothing else. https:// is verified against the system's roots, or against the CA
//     file named by CA alone; http:// is accepted only on a loopback IP address (127.0.0.1, ::1) and only with LoopbackHTTP, for the demo and
//     the qualification.
//   - Bucket and Region name the bucket and its signing region; AccessKey and SecretKey are the credentials, which the caller takes from the
//     environment and nothing here writes anywhere.
//
// No error of this type carries a response body, a key, an endpoint or a credential: a failed request is a *reporting.ObjectStoreError with
// the HTTP status when there was an answer.
type S3 struct {
	Endpoint     string
	Bucket       string
	Region       string
	AccessKey    string
	SecretKey    string
	CA           string // path of a PEM file; with it the bucket's server is verified against these certificates alone
	LoopbackHTTP bool

	ready  bool
	base   url.URL
	host   string
	creds  credentials
	client *http.Client
	now    func() time.Time
}

// String and GoString keep the credentials out of any log line that prints the value.
// (Value receivers: a copy of the struct printed by value is as safe as the pointer.)
func (s S3) String() string   { return "objects.S3{bucket " + s.Bucket + "}" }
func (s S3) GoString() string { return s.String() }

// NewS3 validates the configuration and returns the client. Anything the rules above do not allow is refused here, before a request is made.
func NewS3(c S3) (*S3, error) {
	refuse := func(why string) (*S3, error) {
		return nil, fmt.Errorf("object store configuration: %s: %w", why, r.ErrInvalid)
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && u.Scheme != "http") {
		return refuse("the endpoint must be https://host[:port] (or http:// on a loopback IP), with no user, path, query or fragment")
	}
	host := u.Hostname()
	if host == "" || (u.Port() != "" && !validPort(u.Port())) {
		return refuse("the endpoint's host or port is not valid")
	}
	switch u.Scheme {
	case "http":
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return refuse("an http:// endpoint is allowed only on a loopback IP address")
		}
		if !c.LoopbackHTTP {
			return refuse("an http:// endpoint needs the loopback-http option, which is for the demo and the qualification")
		}
		if c.CA != "" {
			return refuse("a CA file is for an https:// endpoint")
		}
	case "https":
		if c.LoopbackHTTP {
			return refuse("the loopback-http option is for an http:// endpoint")
		}
	}
	switch {
	case !validBucket(c.Bucket):
		return refuse("the bucket name is not a valid S3 bucket name")
	case !validRegion(c.Region):
		return refuse("the region is not valid")
	case !validCredential(c.AccessKey) || !validCredential(c.SecretKey):
		return refuse("the access key and the secret key are required: printable ASCII, no spaces, at most 128 characters")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.DisableCompression = true // the length and the digest are of the bytes as stored, never of a transfer encoding
	if c.CA != "" {
		pem, err := os.ReadFile(c.CA)
		if err != nil {
			return refuse("the CA file cannot be read")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return refuse("the CA file holds no certificate")
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	out := c
	out.ready = true
	out.base = url.URL{Scheme: u.Scheme, Host: u.Host}
	out.host = u.Host
	out.creds = credentials{access: c.AccessKey, secret: c.SecretKey, region: c.Region}
	out.now = time.Now
	out.client = &http.Client{
		Transport: transport,
		Timeout:   RequestTimeout,
		// A redirect is an answer the platform does not follow: the signed request is for this endpoint and no other.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &out, nil
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == p
}

// validBucket: S3's rules for a bucket name, 3 to 63 characters of lower-case letters, digits, hyphens and dots, starting and ending with a
// letter or a digit, no consecutive dots, and not the shape of an IP address.
func validBucket(b string) bool {
	if len(b) < 3 || len(b) > 63 || strings.Contains(b, "..") || net.ParseIP(b) != nil {
		return false
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		edge := i == 0 || i == len(b)-1
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
		case (c == '-' || c == '.') && !edge:
		default:
			return false
		}
	}
	return true
}

func validRegion(s string) bool {
	if len(s) < 1 || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func validCredential(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// validKey: a key is 1..1024 bytes of valid UTF-8 without control characters, made of non-empty segments none of which is . or .. (a server
// may normalize those away and answer for another object).
func validKey(k string) bool {
	if len(k) < 1 || len(k) > 1024 || !utf8.ValidString(k) {
		return false
	}
	for _, c := range k {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	for _, seg := range strings.Split(k, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func validDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == s
}

// response is what a request that was answered leaves: the status, the headers and the open body, which the caller reads and closes.
type response struct {
	status int
	header http.Header
	body   io.ReadCloser
}

func (s *S3) objectPath(key string) string { return "/" + s.Bucket + "/" + key }

// do signs and sends one request, retrying once, with a signature of its own, when a network error (no answer at all) interrupted it: every
// request of this client is idempotent. A request that was answered is never retried, whatever the status. The error of a request that could
// not be sent carries nothing of the transport's: its text names the endpoint.
func (s *S3) do(ctx context.Context, method, path string, query [][2]string, body []byte, payloadHash string) (*response, error) {
	if s == nil || !s.ready {
		return nil, errors.New("object store: the client was not made by NewS3")
	}
	for attempt := 0; ; attempt++ {
		resp, err := s.once(ctx, method, path, query, body, payloadHash)
		if err == nil {
			return resp, nil
		}
		// One retry, and only of a connection that failed: not when the caller gave up, and not on a timeout, which a second wait would
		// only double (the store is slow, not gone).
		var timeout interface{ Timeout() bool }
		if attempt == 1 || ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
			return nil, &r.ObjectStoreError{}
		}
	}
}

func (s *S3) once(ctx context.Context, method, path string, query [][2]string, body []byte, payloadHash string) (*response, error) {
	at := s.now().UTC()
	headers := map[string]string{"x-amz-content-sha256": payloadHash, "x-amz-date": at.Format(dateLayout)}
	d := derive(s.creds, method, s.host, path, query, headers, payloadHash, at)
	u := s.base
	u.Path = path
	u.RawPath = encodePath(path)
	u.RawQuery = encodeQuery(query)
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Host = s.host
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-date", headers["x-amz-date"])
	req.Header.Set("Authorization", authorization(s.creds, d, at))
	resp, err := s.client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	return &response{status: resp.StatusCode, header: resp.Header, body: &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}}, nil
}

// cancelOnClose ends the request's context with its body, so that the timeout covers the read and nothing leaks.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error { err := c.ReadCloser.Close(); c.cancel(); return err }

// finish drains a little of a body the caller does not read, so that the connection can be reused, and closes it.
func (p *response) finish() {
	_, _ = io.Copy(io.Discard, io.LimitReader(p.body, 4096))
	_ = p.body.Close()
}

// Check is HEAD on the bucket: any answer but 200 is the store not being usable, with the status.
func (s *S3) Check(ctx context.Context) error {
	resp, err := s.do(ctx, http.MethodHead, "/"+s.Bucket, nil, nil, emptySHA256)
	if err != nil {
		return err
	}
	defer resp.finish()
	if resp.status != http.StatusOK {
		return &r.ObjectStoreError{Status: resp.status}
	}
	return nil
}

// Put stores the content under the key, sending its SHA-256 as x-amz-content-sha256 so that the server refuses a body that arrived altered.
// sha256 must be the hex digest of content: a caller whose digest is not the content's is wrong, and nothing is sent.
func (s *S3) Put(ctx context.Context, key string, content []byte, sha string) error {
	if !validKey(key) || len(content) == 0 || len(content) > r.MaxArtifactBytes || !validDigest(sha) {
		return r.ErrInvalid
	}
	if sum := sha256.Sum256(content); hex.EncodeToString(sum[:]) != sha {
		return r.ErrInvalid
	}
	resp, err := s.do(ctx, http.MethodPut, s.objectPath(key), nil, content, sha)
	if err != nil {
		return err
	}
	defer resp.finish()
	if resp.status != http.StatusOK {
		return &r.ObjectStoreError{Status: resp.status}
	}
	return nil
}

// Get returns the object, which must be length bytes with the digest sha: a missing object, another length (the header's or the body's) or
// another digest is r.ErrIntegrity, and no byte of such an object is returned. It reads at most length+1 bytes, and length is at most
// r.MaxArtifactBytes.
func (s *S3) Get(ctx context.Context, key, sha string, length int) ([]byte, error) {
	if !validKey(key) || length < 1 || length > r.MaxArtifactBytes || !validDigest(sha) {
		return nil, r.ErrInvalid
	}
	resp, err := s.do(ctx, http.MethodGet, s.objectPath(key), nil, nil, emptySHA256)
	if err != nil {
		return nil, err
	}
	defer resp.finish()
	switch resp.status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("the object is missing: %w", r.ErrIntegrity)
	default:
		return nil, &r.ObjectStoreError{Status: resp.status}
	}
	if cl := resp.header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err != nil || n != int64(length) {
			return nil, fmt.Errorf("the object's length is not the row's: %w", r.ErrIntegrity)
		}
	}
	b, err := io.ReadAll(io.LimitReader(resp.body, int64(length)+1))
	if err != nil {
		// The connection broke or the timeout ran out while the body was read: the store was not reachable for this read.
		return nil, &r.ObjectStoreError{}
	}
	if len(b) != length {
		return nil, fmt.Errorf("the object's length is not the row's: %w", r.ErrIntegrity)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != sha {
		return nil, fmt.Errorf("the object's digest is not the row's: %w", r.ErrIntegrity)
	}
	return b, nil
}

// Delete removes the object. An object that is not there is not an error (S3 answers 204 for it; a server that answers 404 is believed).
func (s *S3) Delete(ctx context.Context, key string) error {
	if !validKey(key) {
		return r.ErrInvalid
	}
	resp, err := s.do(ctx, http.MethodDelete, s.objectPath(key), nil, nil, emptySHA256)
	if err != nil {
		return err
	}
	defer resp.finish()
	switch resp.status {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	}
	return &r.ObjectStoreError{Status: resp.status}
}

type listResult struct {
	XMLName               xml.Name `xml:"ListBucketResult"`
	IsTruncated           bool     `xml:"IsTruncated"`
	NextContinuationToken string   `xml:"NextContinuationToken"`
	Contents              []struct {
		Key          string `xml:"Key"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
}

// List is ListObjectsV2 on the prefix: at most limit (1..MaxListLimit) keys in key order, from the continuation token after ("" for the
// first page), and the token of the next page ("" when there is none). A listing the platform cannot read, or one that names a key outside the
// prefix, is the store being unusable.
func (s *S3) List(ctx context.Context, prefix, after string, limit int) ([]r.Object, string, error) {
	if limit < 1 || limit > MaxListLimit || len(prefix) > 1024 || len(after) > 2048 || !utf8.ValidString(prefix) {
		return nil, "", r.ErrInvalid
	}
	query := [][2]string{{"list-type", "2"}, {"max-keys", strconv.Itoa(limit)}}
	if prefix != "" {
		query = append(query, [2]string{"prefix", prefix})
	}
	if after != "" {
		query = append(query, [2]string{"continuation-token", after})
	}
	resp, err := s.do(ctx, http.MethodGet, "/"+s.Bucket, query, nil, emptySHA256)
	if err != nil {
		return nil, "", err
	}
	defer resp.finish()
	if resp.status != http.StatusOK {
		return nil, "", &r.ObjectStoreError{Status: resp.status}
	}
	b, err := io.ReadAll(io.LimitReader(resp.body, maxListBody+1))
	if err != nil {
		return nil, "", &r.ObjectStoreError{}
	}
	bad := &r.ObjectStoreError{Status: resp.status}
	var parsed listResult
	if len(b) > maxListBody || xml.Unmarshal(b, &parsed) != nil || len(parsed.Contents) > limit {
		return nil, "", bad
	}
	out := make([]r.Object, 0, len(parsed.Contents))
	for _, c := range parsed.Contents {
		at, err := time.Parse(time.RFC3339, c.LastModified)
		if err != nil || !strings.HasPrefix(c.Key, prefix) || c.Key == "" {
			return nil, "", bad
		}
		out = append(out, r.Object{Key: c.Key, LastModified: at.UTC()})
	}
	next := ""
	if parsed.IsTruncated {
		if parsed.NextContinuationToken == "" || len(parsed.NextContinuationToken) > 2048 {
			return nil, "", bad
		}
		next = parsed.NextContinuationToken
	}
	return out, next, nil
}

var _ r.ObjectStore = (*S3)(nil)

// Close gives back the idle connections the client keeps to the object store (the server calls it when it has drained, so that no
// connection goroutine outlives the process's own shutdown). The client stays usable: a later request dials again.
func (s *S3) Close() {
	if s != nil && s.client != nil {
		s.client.CloseIdleConnections()
	}
}
