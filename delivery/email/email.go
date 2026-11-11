// Package email delivers the end of a scheduled run to its owner by mail (S16-B, docs/S16B_SCOPE.md, the amendment of
// docs/ADR_0020_SCHEDULED_REPORTS.md): the same notification the webhook carries, written for a person, with the schedule's name (the owner
// wrote it), the occurrence, the outcome and its code, the run's and the schedule's ids, and a link to the console, where the owner downloads
// the file with their own access. No row, no query, no attachment, no artifact content: the mail says that a run ended and how, and nothing
// of the report. The recipient is the owner's address in the bindings file and nothing else: no route writes it, a notification carries no
// address, and an owner whose bindings name none gets no mail and nothing is recorded of it. SMTP is the standard library's (net/smtp): STARTTLS
// to the configured host (plain SMTP only to a loopback IP address, for tests), PLAIN authentication only over TLS or to loopback, the
// password from the environment. Nothing of the address, the name, the body or the password is logged or returned: one outcome word and the
// server's reply code.
package email

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	r "idp.local/reporting/reporting"
)

// PasswordEnv is the environment variable that carries the SMTP password, with a username.
const PasswordEnv = "REPORTING_SMTP_PASSWORD"

// The defaults and bounds.
const (
	DefaultPort    = 587
	DefaultTimeout = 10 * time.Second
	MaxTimeout     = 30 * time.Second
	// MaxRecipients bounds the addresses one notification is mailed to (the bindings' own bound, accessjwt.MaxEmails).
	MaxRecipients = 4
	// MaxAddressBytes bounds one address (RFC 5321's forward-path).
	MaxAddressBytes  = 254
	maxHostBytes     = 253
	maxUsernameBytes = 256
	maxPasswordBytes = 1024
	maxURLBytes      = 2048
)

// The headers a mail carries beside the usual ones, as the webhook's.
const (
	DeliveryHeader = "X-Reporting-Delivery" // the run's id: a person's or a filter's key for a mail that comes twice
	EventHeader    = "X-Reporting-Event"    // reporting.NotificationEvent
)

// ErrConfig is the one refusal of a configuration; its text names no value.
var ErrConfig = errors.New("email: the host (a name or an IP address, no port), the port (1 to 65535), the from address (one RFC 5322 addr-spec), the console url (https, or http to a loopback IP address with loopback_plain), starttls (false only to a loopback IP address with loopback_plain), the timeout (1 to 30 seconds), the username with its password (1 to 1024 bytes, from the environment) or the recipients function is refused")

// Config is the SMTP server, the sender, the links and the timeout of one attempt.
type Config struct {
	Host string
	Port int // 0: DefaultPort
	// From is the sender's address, an addr-spec.
	From string
	// Username, when set, authenticates with SASL PLAIN, with Password (the environment's PasswordEnv); PLAIN goes only over TLS or to a
	// loopback IP address. A password without a username is refused.
	Username, Password string
	// StartTLS requires the server to offer STARTTLS and speaks TLS from then on; false sends in the clear and is allowed only to a loopback IP
	// address together with LoopbackPlain (tests), as the webhook's loopback_http.
	StartTLS      bool
	LoopbackPlain bool
	Timeout       time.Duration // 0: DefaultTimeout
	// ConsoleURL is the console's base the links use (<ConsoleURL>/console/#schedules): https, or http to a loopback IP address with LoopbackPlain.
	ConsoleURL string
	// Recipients answers the addresses a run's owner is mailed at (the bindings in force: accessjwt.Verifier.Emails); none for an owner whose
	// bindings name no address, and such a run is delivered without a mail. Required.
	Recipients func(owner r.Identity) []string
	// Names answers the schedule's name for its owner (the store's owner-scoped read). Optional: without one, or when it fails, the mail names
	// the schedule by its id.
	Names func(ctx context.Context, owner r.Identity, scheduleID string) (string, error)
	// TLS replaces the STARTTLS configuration (tests: a server with a certificate of its own); nil is the host's name and TLS 1.2 or later.
	TLS *tls.Config
	// Now is the clock of the Date header; nil is time.Now.
	Now func() time.Time
}

// Mailer is a configured sender.
type Mailer struct {
	addr, host string
	from       string
	auth       smtp.Auth
	startTLS   bool
	tls        *tls.Config
	timeout    time.Duration
	console    string
	recipients func(r.Identity) []string
	names      func(context.Context, r.Identity, string) (string, error)
	now        func() time.Time
}

// ValidAddress says whether s is one address the platform mails to or from: an RFC 5322 addr-spec as net/mail reads it, written bare (no
// display name, angle brackets or comment), of printable ASCII (SMTP without SMTPUTF8 carries nothing else), at most MaxAddressBytes. The
// bindings' email is held to the same rule (identity/accessjwt).
func ValidAddress(s string) bool {
	if s == "" || len(s) > MaxAddressBytes || !printable(s) {
		return false
	}
	a, err := mail.ParseAddress(s)
	return err == nil && a.Name == "" && a.Address == s
}

// ValidHost says whether h is an SMTP host the platform connects to: an IP address, or a DNS name of labels of letters, digits and hyphens
// separated by dots, at most 253 characters; no port, no scheme, no path.
func ValidHost(h string) bool {
	if h == "" || len(h) > maxHostBytes {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return true
	}
	for _, label := range strings.Split(strings.TrimSuffix(h, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// ValidConsoleURL says whether u is the console's base the links use: https with a host, no user information, query or fragment, at most
// 2048 bytes; http to a loopback IP address when loopbackPlain.
func ValidConsoleURL(u string, loopbackPlain bool) bool {
	if u == "" || len(u) > maxURLBytes || !printable(u) {
		return false
	}
	p, err := url.Parse(u)
	if err != nil || p.Host == "" || p.User != nil || p.RawQuery != "" || p.ForceQuery || p.Fragment != "" || strings.Contains(u, "#") || p.Opaque != "" {
		return false
	}
	switch p.Scheme {
	case "https":
		return true
	case "http":
		return loopbackPlain && loopbackIP(p.Hostname())
	}
	return false
}

func loopbackIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// printable says whether s is text of printable ASCII without a space.
func printable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// secretText says whether s is a password: valid UTF-8 without control characters, 1 to max bytes.
func secretText(s string, max int) bool {
	if s == "" || len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if c < 0x20 || c == 0x7f || c == utf8.RuneError {
			return false
		}
	}
	return true
}

// New checks the configuration.
func New(c Config) (*Mailer, error) {
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	loopback := loopbackIP(c.Host)
	switch {
	case !ValidHost(c.Host), c.Port < 1 || c.Port > 65535, !ValidAddress(c.From), !ValidConsoleURL(c.ConsoleURL, c.LoopbackPlain),
		c.Timeout < time.Second || c.Timeout > MaxTimeout, c.Recipients == nil,
		!c.StartTLS && !(c.LoopbackPlain && loopback),
		(c.Username == "") != (c.Password == ""),
		c.Username != "" && (len(c.Username) > maxUsernameBytes || !printable(c.Username) || !secretText(c.Password, maxPasswordBytes)):
		return nil, ErrConfig
	}
	m := &Mailer{addr: net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), host: c.Host, from: c.From, startTLS: c.StartTLS, tls: c.TLS, timeout: c.Timeout,
		console: strings.TrimRight(c.ConsoleURL, "/"), recipients: c.Recipients, names: c.Names, now: c.Now}
	if m.tls == nil {
		m.tls = &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	}
	if c.Username != "" {
		// smtp.PlainAuth refuses, on its own, to send the password over a connection that is neither TLS nor to localhost.
		m.auth = smtp.PlainAuth("", c.Username, c.Password, c.Host)
	}
	return m, nil
}

// Deliver implements reporting.Deliverer: one attempt. An owner whose bindings name no address is delivered without a mail.
func (m *Mailer) Deliver(ctx context.Context, n r.Notification) r.DeliveryResult {
	owner := r.Identity{Tenant: n.Tenant, Issuer: n.Issuer, Subject: n.Subject}
	to := m.addresses(owner)
	if len(to) == 0 {
		return r.DeliveryResult{Outcome: r.DeliveryOutcomeDelivered}
	}
	name := ""
	if m.names != nil {
		if v, err := m.names(ctx, owner, n.ScheduleID); err == nil {
			name = v
		}
	}
	return m.send(ctx, to, compose(n, name, m.from, to, m.console, m.now()))
}

// addresses is the owner's recipients, each a valid address, distinct, at most MaxRecipients.
func (m *Mailer) addresses(owner r.Identity) []string {
	var out []string
	for _, a := range m.recipients(owner) {
		if ValidAddress(a) && !slices.Contains(out, a) {
			out = append(out, a)
		}
		if len(out) == MaxRecipients {
			break
		}
	}
	return out
}

// send is one SMTP session within the timeout: connect, EHLO, STARTTLS when required (nothing goes in the clear when the server does not
// offer it), AUTH when configured, MAIL, RCPT for each address, DATA, QUIT.
func (m *Mailer) send(ctx context.Context, to []string, msg []byte) r.DeliveryResult {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", m.addr)
	if err != nil {
		return unreachable
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return outcomeOf(err)
	}
	defer c.Close()
	if m.startTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return unreachable
		}
		if err := c.StartTLS(m.tls); err != nil {
			return outcomeOf(err)
		}
	}
	if m.auth != nil {
		if err := c.Auth(m.auth); err != nil {
			return outcomeOf(err)
		}
	}
	if err := c.Mail(m.from); err != nil {
		return outcomeOf(err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return outcomeOf(err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return outcomeOf(err)
	}
	if _, err := w.Write(msg); err != nil {
		return outcomeOf(err)
	}
	if err := w.Close(); err != nil {
		return outcomeOf(err)
	}
	_ = c.Quit()
	return r.DeliveryResult{Outcome: r.DeliveryOutcomeDelivered, Status: 250}
}

var unreachable = r.DeliveryResult{Outcome: r.DeliveryOutcomeUnreachable}

// outcomeOf is the outcome word of a failed step: a reply 4yz (a transient negative completion: try again later) is throttled, 5yz (a permanent
// one: the sender, a recipient or the credentials refused) is rejected, another reply out of the protocol's order is server_error; no reply (the
// connection, TLS, the timeout) is unreachable. The status is the reply's code.
func outcomeOf(err error) r.DeliveryResult {
	var reply *textproto.Error
	if !errors.As(err, &reply) {
		return unreachable
	}
	switch {
	case reply.Code >= 400 && reply.Code < 500:
		return r.DeliveryResult{Outcome: r.DeliveryOutcomeThrottled, Status: reply.Code}
	case reply.Code >= 500:
		return r.DeliveryResult{Outcome: r.DeliveryOutcomeRejected, Status: reply.Code}
	}
	return r.DeliveryResult{Outcome: r.DeliveryOutcomeServerError, Status: reply.Code}
}

// outcomeWords are what the mail calls a run's end, by its state.
var outcomeWords = map[string]string{r.RunSucceeded: "成功", r.RunFailed: "失败", r.RunRefused: "被拒绝", r.RunCancelled: "已取消"}

// compose is the message: headers of ASCII (the subject RFC 2047 encoded, so that nothing the person wrote reaches a header as it is), a
// text/plain body in UTF-8, base64 encoded. It carries what the notification says and the schedule's name, and nothing else.
func compose(n r.Notification, name, from string, to []string, console string, now time.Time) []byte {
	label := name
	if label == "" {
		label = n.ScheduleID
	}
	word := outcomeWords[n.State]
	if word == "" {
		word = n.State
	}
	result := n.State
	if n.ErrorCode != "" {
		result += "，" + n.ErrorCode
	}
	var body strings.Builder
	fmt.Fprintf(&body, "您的定时报表「%s」的一次运行已%s。\n\n", label, word)
	fmt.Fprintf(&body, "计划名称：%s\n", label)
	fmt.Fprintf(&body, "触发时间（UTC）：%s\n", n.DueAt)
	fmt.Fprintf(&body, "结束时间（UTC）：%s\n", n.FinishedAt)
	fmt.Fprintf(&body, "结果：%s（%s）\n", word, result)
	if n.Format != "" {
		fmt.Fprintf(&body, "导出格式：%s\n", n.Format)
	}
	fmt.Fprintf(&body, "运行编号：%s\n", n.RunID)
	fmt.Fprintf(&body, "计划编号：%s\n\n", n.ScheduleID)
	fmt.Fprintf(&body, "在控制台查看这次运行，并以您本人的访问权限下载文件：\n%s/console/#schedules\n\n", console)
	body.WriteString("本邮件只说明运行已结束：不含报表的任何数据，没有附件；文件只能在平台内以您本人的权限读取。\n")
	body.WriteString("此邮件由 IDP Reporting 自动发送，请勿回复。\n")

	domain := from[strings.LastIndexByte(from, '@')+1:]
	subject := mime.BEncoding.Encode("utf-8", fmt.Sprintf("[IDP Reporting] 定时报表 %s · %s", label, word))
	var msg strings.Builder
	header := func(k, v string) { msg.WriteString(k + ": " + v + "\r\n") }
	header("From", from)
	header("To", strings.Join(to, ", "))
	header("Subject", strings.ReplaceAll(subject, " =?", "\r\n =?"))
	header("Date", now.UTC().Format(time.RFC1123Z))
	header("Message-ID", fmt.Sprintf("<schedule-run.%s.%d@%s>", n.RunID, n.Attempt, domain))
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "base64")
	header("Auto-Submitted", "auto-generated")
	header(DeliveryHeader, n.RunID)
	header(EventHeader, n.Event)
	msg.WriteString("\r\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(body.String()))
	for len(encoded) > 76 {
		msg.WriteString(encoded[:76] + "\r\n")
		encoded = encoded[76:]
	}
	msg.WriteString(encoded + "\r\n")
	return []byte(msg.String())
}

var _ r.Deliverer = (*Mailer)(nil)
