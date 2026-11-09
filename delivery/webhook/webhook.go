// Package webhook delivers the end of a scheduled run to the deployment's endpoint (S16-A, docs/S16A_SCOPE.md, docs/ADR_0020_SCHEDULED_REPORTS.md):
// one POST of the notification's JSON, signed with HMAC-SHA256 over the exact body under a secret the deployment gives in its environment. The
// endpoint is https (plain http only to a loopback IP address, for tests); a redirect is never followed; nothing of the answer is read beyond its
// status and a bounded drain; nothing of the endpoint, the secret or the answer is logged or returned, only an outcome word and the status.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	r "idp.local/reporting/reporting"
)

// SecretEnv is the environment variable that carries the signing secret.
const SecretEnv = "REPORTING_WEBHOOK_SECRET"

// The bounds.
const (
	MinSecretBytes = 32
	MaxSecretBytes = 1024
	DefaultTimeout = 10 * time.Second
	MaxTimeout     = 30 * time.Second
	maxURLBytes    = 2048
	// drainBytes bounds what is read of an answer, so that the connection can be reused; the rest is discarded with the connection.
	drainBytes = 64 << 10
)

// The headers of a delivery.
const (
	SignatureHeader = "X-Reporting-Signature" // sha256=<hex HMAC-SHA256 of the body>
	DeliveryHeader  = "X-Reporting-Delivery"  // the run's id: the receiver's key for at-least-once delivery
	EventHeader     = "X-Reporting-Event"     // reporting.NotificationEvent
)

// ErrConfig is the one refusal of a configuration; its text names no value.
var ErrConfig = errors.New("webhook: the url (https, or http to a loopback IP address with loopback_http), the secret (32 to 1024 bytes) or the timeout (1 to 30 seconds) is refused")

// Config is the endpoint, the secret and the timeout of one attempt.
type Config struct {
	URL          string
	Secret       []byte
	Timeout      time.Duration // 0: DefaultTimeout
	LoopbackHTTP bool
	// Transport replaces the client's transport (tests: a server with a certificate of its own); nil is a clone of the default.
	Transport http.RoundTripper
}

// Webhook is a configured endpoint.
type Webhook struct {
	url    string
	secret []byte
	client *http.Client
}

// ValidURL says whether u is an endpoint the platform delivers to: https with a host, or http to a loopback IP address when loopbackHTTP; no
// user information, no fragment.
func ValidURL(u string, loopbackHTTP bool) bool {
	if u == "" || len(u) > maxURLBytes {
		return false
	}
	p, err := url.Parse(u)
	if err != nil || p.Host == "" || p.User != nil || p.Fragment != "" || strings.Contains(u, "#") || p.Opaque != "" {
		return false
	}
	switch p.Scheme {
	case "https":
		return true
	case "http":
		ip := net.ParseIP(p.Hostname())
		return loopbackHTTP && ip != nil && ip.IsLoopback()
	}
	return false
}

// New checks the configuration.
func New(c Config) (*Webhook, error) {
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	if !ValidURL(c.URL, c.LoopbackHTTP) || len(c.Secret) < MinSecretBytes || len(c.Secret) > MaxSecretBytes || c.Timeout < time.Second || c.Timeout > MaxTimeout {
		return nil, ErrConfig
	}
	transport := c.Transport
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.MaxIdleConns, t.MaxIdleConnsPerHost = 4, 4
		transport = t
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   c.Timeout,
		// A redirect is never followed: the endpoint is the one the deployment named, and a signed body is not sent anywhere else.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Webhook{url: c.URL, secret: append([]byte(nil), c.Secret...), client: client}, nil
}

// Sign is the signature header's value for body under secret.
func Sign(secret, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Verify is the receiver's check of a delivery: the header is the signature of the body under the secret, compared in constant time.
func Verify(secret, body []byte, header string) bool {
	return hmac.Equal([]byte(header), []byte(Sign(secret, body)))
}

// Deliver implements reporting.Deliverer: one attempt.
func (w *Webhook) Deliver(ctx context.Context, n r.Notification) r.DeliveryResult {
	body, err := json.Marshal(n)
	if err != nil {
		return r.DeliveryResult{Outcome: r.DeliveryOutcomeRejected}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return r.DeliveryResult{Outcome: r.DeliveryOutcomeUnreachable}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "idp-reporting-webhook/1")
	req.Header.Set(SignatureHeader, Sign(w.secret, body))
	req.Header.Set(DeliveryHeader, n.RunID)
	req.Header.Set(EventHeader, n.Event)
	res, err := w.client.Do(req)
	if err != nil {
		return r.DeliveryResult{Outcome: r.DeliveryOutcomeUnreachable}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, drainBytes))
	_ = res.Body.Close()
	return r.DeliveryResult{Outcome: OutcomeOf(res.StatusCode), Status: res.StatusCode}
}

// OutcomeOf is the outcome word of an answer's status.
func OutcomeOf(status int) string {
	switch {
	case status >= 200 && status < 300:
		return r.DeliveryOutcomeDelivered
	case status >= 300 && status < 400:
		return r.DeliveryOutcomeRedirected
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests:
		return r.DeliveryOutcomeThrottled
	case status >= 500:
		return r.DeliveryOutcomeServerError
	}
	return r.DeliveryOutcomeRejected
}

var _ r.Deliverer = (*Webhook)(nil)
