package server

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"idp.local/reporting/identity/accessjwt"
)

// File kinds, by what they may be readable by.
const (
	secretFile = iota // a secret: a regular file, owned by the process's user, mode 0600 or 0400
	publicFile        // not secret but trusted: a regular file owned by the process's user or root, not writable by group or others
)

const (
	maxKeyFileBytes      = 4 << 10
	maxJWKSFileBytes     = 64 << 10 // accessjwt's own bound
	maxBindingsFileBytes = 4 << 20
	maxCertFileBytes     = 64 << 10
	minCursorKeyBytes    = 32
	maxCursorKeyBytes    = 64
)

// fileProblem is a refusal about one file named by a field of the configuration. Its text names the field and the rule; the path is the
// operator's own text from the file, and is not repeated, so that an error never carries it into a log by accident.
type fileProblem struct{ field, rule string }

func (e *fileProblem) Error() string { return fmt.Sprintf("%s: %s", e.field, e.rule) }

// readTrustedFile reads a file named by the configuration after the checks of its kind: a symlink is refused (the file is opened with
// O_NOFOLLOW, so there is no window between the check and the read), as is anything but a regular file, a file of another user, a mode
// that lets another user read a secret or write anything, and a size above max. field is the configuration field that named it.
func readTrustedFile(field, path string, kind int, max int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, &fileProblem{field, "the file is a symbolic link, which is refused"}
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, &fileProblem{field, "the file does not exist"}
		}
		return nil, &fileProblem{field, "the file cannot be opened"}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, &fileProblem{field, "the file cannot be examined"}
	}
	if !info.Mode().IsRegular() {
		return nil, &fileProblem{field, "the file is not a regular file"}
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, &fileProblem{field, "the file's owner cannot be determined"}
	}
	self := uint32(os.Geteuid())
	perm := info.Mode().Perm()
	switch kind {
	case secretFile:
		if st.Uid != self {
			return nil, &fileProblem{field, "the file is not owned by the user the server runs as"}
		}
		if perm != 0o600 && perm != 0o400 {
			return nil, &fileProblem{field, "the file's mode must be 0600 or 0400"}
		}
	default:
		if st.Uid != self && st.Uid != 0 {
			return nil, &fileProblem{field, "the file is owned by neither the user the server runs as nor root"}
		}
		if perm&0o022 != 0 {
			return nil, &fileProblem{field, "the file is writable by its group or by others"}
		}
	}
	if info.Size() > max {
		return nil, &fileProblem{field, fmt.Sprintf("the file is larger than %d bytes", max)}
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(data)) > max {
		return nil, &fileProblem{field, "the file cannot be read within its size bound"}
	}
	return data, nil
}

// decodeKey reads a cursor key file: the key hex- or base64-encoded (standard or URL alphabet, padded or not), surrounding white space
// ignored, 32 to 64 bytes. A text that is valid as both is read as hex.
func decodeKey(text []byte) ([]byte, bool) {
	s := strings.TrimSpace(string(text))
	if b, err := hex.DecodeString(s); err == nil {
		return b, len(b) >= minCursorKeyBytes && len(b) <= maxCursorKeyBytes
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.Strict().DecodeString(s); err == nil {
			return b, len(b) >= minCursorKeyBytes && len(b) <= maxCursorKeyBytes
		}
	}
	return nil, false
}

// readCursorKeys reads the ring, newest first. Two files holding the same key are refused.
func readCursorKeys(files []string) ([][]byte, error) {
	ring := make([][]byte, 0, len(files))
	for i, path := range files {
		field := fmt.Sprintf("cursor_key_files[%d]", i)
		data, err := readTrustedFile(field, path, secretFile, maxKeyFileBytes)
		if err != nil {
			return nil, err
		}
		key, ok := decodeKey(data)
		if !ok {
			return nil, &fileProblem{field, "the file must hold one key, hex or base64 encoded, of 32 to 64 bytes"}
		}
		for _, other := range ring {
			if bytes.Equal(other, key) {
				return nil, &fileProblem{field, "the file holds the same key as another file of the ring"}
			}
		}
		ring = append(ring, key)
	}
	return ring, nil
}

// bindingEntry is one line of the bindings file.
type bindingEntry struct {
	Subject      string   `json:"subject"`
	ClientID     string   `json:"client_id"`
	Tenant       string   `json:"tenant"`
	Integrations []string `json:"integrations"`
}

// issuerFiles are the two files a reload re-reads.
type issuerFiles struct {
	jwks     []byte
	bindings []accessjwt.Binding
}

// Codes of a failed read of the issuer files: what a log line says, in place of the file's path or content.
const (
	CodeIssuerFileUnreadable = "ISSUER_FILE_UNREADABLE" // missing, a symlink, not regular, too large, of another user, writable by others
	CodeIssuerFileInvalid    = "ISSUER_FILE_INVALID"    // the bindings file is not the strict list of bindings
	CodeIssuerConfigRefused  = "ISSUER_CONFIG_REFUSED"  // accessjwt refused the whole configuration (the JWKS, a binding, a duplicate)
)

// readIssuerFiles reads the keys and the bindings. The returned error is a *fileProblem or a *reloadError carrying a code.
func readIssuerFiles(c IssuerConfig) (issuerFiles, string, error) {
	jwks, err := readTrustedFile("issuer.jwks_file", c.JWKSFile, publicFile, maxJWKSFileBytes)
	if err != nil {
		return issuerFiles{}, CodeIssuerFileUnreadable, err
	}
	raw, err := readTrustedFile("issuer.bindings_file", c.BindingsFile, publicFile, maxBindingsFileBytes)
	if err != nil {
		return issuerFiles{}, CodeIssuerFileUnreadable, err
	}
	list, err := parseBindings(raw)
	if err != nil {
		return issuerFiles{}, CodeIssuerFileInvalid, err
	}
	return issuerFiles{jwks: jwks, bindings: list}, "", nil
}

// parseBindings reads the bindings file strictly: a JSON list of {subject, client_id, tenant, integrations}, no unknown or repeated field,
// nothing after the list.
func parseBindings(raw []byte) ([]accessjwt.Binding, error) {
	bad := func(rule string) ([]accessjwt.Binding, error) {
		return nil, &fileProblem{"issuer.bindings_file", rule}
	}
	if len(raw) > maxBindingsFileBytes || !isUTF8(raw) {
		return bad("the file is not a valid UTF-8 document within its bound")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tree, err := readTree(dec, 0)
	if err != nil {
		return bad("the file is not valid JSON without repeated keys")
	}
	if _, err := dec.Token(); err != io.EOF {
		return bad("data follows the list")
	}
	list, ok := tree.([]any)
	if !ok {
		return bad("the file must be a JSON list of bindings")
	}
	if len(list) > 4096 {
		return bad("the file holds more than 4096 bindings")
	}
	allowed := map[string]bool{"subject": true, "client_id": true, "tenant": true, "integrations": true}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return bad("every binding must be an object")
		}
		for key := range m {
			if !allowed[key] {
				return bad("a binding has a field the schema does not know")
			}
		}
	}
	var entries []bindingEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return bad("a binding has a field of the wrong type")
	}
	out := make([]accessjwt.Binding, 0, len(entries))
	for _, e := range entries {
		out = append(out, accessjwt.Binding{Subject: e.Subject, ClientID: e.ClientID, Tenant: e.Tenant, Integrations: e.Integrations})
	}
	return out, nil
}

func isUTF8(b []byte) bool { return strings.ToValidUTF8(string(b), "\x00") == string(b) }

// issuerConfig is the accessjwt configuration for the files read and the issuer section.
func issuerConfig(c IssuerConfig, f issuerFiles) accessjwt.Config {
	return accessjwt.Config{Issuer: c.Issuer, Audience: c.Audience, RequiredScope: c.RequiredScope, JWKS: f.jwks, MaxTokenLifetime: seconds(c.MaxTokenLifetime), Bindings: f.bindings}
}

// newVerifier builds the verifier from the files. The error is fixed text: accessjwt's is already free of key and subject data, and says only
// that the configuration is invalid.
func newVerifier(c IssuerConfig) (*accessjwt.Verifier, issuerFiles, error) {
	files, _, err := readIssuerFiles(c)
	if err != nil {
		return nil, issuerFiles{}, err
	}
	v, err := accessjwt.New(issuerConfig(c, files))
	if err != nil {
		return nil, issuerFiles{}, &fileProblem{"issuer", "the issuer, the audience, the scope, the lifetime, the keys or the bindings are refused by the access-token profile (https issuer; RSA 2048-4096 keys with e=65537, at most 16; unique kids; bindings with unique subject and client, at most 32 integrations each)"}
	}
	return v, files, nil
}

// loadTLS reads the certificate and its private key and returns a TLS 1.3 only configuration. The key file is a secret file; the certificate
// is public (not writable by others) and must be valid now.
func loadTLS(cert, key string, now time.Time) (*tls.Config, error) {
	certPEM, err := readTrustedFile("tls_cert_file", cert, publicFile, maxCertFileBytes)
	if err != nil {
		return nil, err
	}
	keyPEM, err := readTrustedFile("tls_key_file", key, secretFile, maxKeyFileBytes)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, &fileProblem{"tls_cert_file", "the certificate and the private key do not form a pair"}
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, &fileProblem{"tls_cert_file", "the certificate cannot be parsed"}
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil, &fileProblem{"tls_cert_file", "the certificate is not valid now"}
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}, nil
}

// parseLeaf returns the certificate's NotAfter.
func parseLeaf(der []byte) (time.Time, error) {
	c, err := x509.ParseCertificate(der)
	if err != nil {
		return time.Time{}, err
	}
	return c.NotAfter, nil
}
