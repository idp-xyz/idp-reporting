package objects

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// AWS Signature Version 4 for the S3 API (S04-E, docs/ADR_0007_OBJECT_STORAGE.md), written against the specification: the canonical request,
// the string to sign, the signing key and the Authorization header. Only what the five operations need: the signature is carried in the
// Authorization header, the payload is hashed in one piece (x-amz-content-sha256 is the hex SHA-256 of the body, never UNSIGNED-PAYLOAD and
// never chunked), and the keys are path-style. For S3 a path is URI-encoded once (not twice, as other AWS services do).

const (
	algorithm  = "AWS4-HMAC-SHA256"
	service    = "s3"
	dateLayout = "20060102T150405Z"
)

// emptySHA256 is the SHA-256 of no bytes: the payload hash of every request without a body.
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// credentials are what a request is signed with.
type credentials struct{ access, secret, region string }

// uriEncode is the specification's URI encoding: every byte but the unreserved characters (A-Z a-z 0-9 - _ . ~) is %XX in upper case, and
// the slash is encoded too unless it separates the segments of a path (encodePath).
func uriEncode(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

// encodePath is the canonical URI of an absolute path: each segment encoded, the slashes kept.
func encodePath(path string) string {
	segments := strings.Split(path, "/")
	for i, s := range segments {
		segments[i] = uriEncode(s)
	}
	return strings.Join(segments, "/")
}

// encodeQuery is the canonical query string: names and values encoded, the pairs sorted by name and then value. It is also the query the
// request is sent with, so that what is signed is what is sent.
func encodeQuery(pairs [][2]string) string {
	encoded := make([][2]string, len(pairs))
	for i, p := range pairs {
		encoded[i] = [2]string{uriEncode(p[0]), uriEncode(p[1])}
	}
	sort.Slice(encoded, func(i, j int) bool {
		if encoded[i][0] != encoded[j][0] {
			return encoded[i][0] < encoded[j][0]
		}
		return encoded[i][1] < encoded[j][1]
	})
	parts := make([]string, len(encoded))
	for i, p := range encoded {
		parts[i] = p[0] + "=" + p[1]
	}
	return strings.Join(parts, "&")
}

// trimValue is a header value as the specification canonicalizes it: no leading or trailing space, runs of spaces made one.
func trimValue(v string) string { return strings.Join(strings.Fields(v), " ") }

// derivation is every step of a signature, so that the qualification can compare each with the specification's published example.
type derivation struct {
	CanonicalRequest string
	SignedHeaders    string
	StringToSign     string
	Signature        string
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func hashHex(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// derive signs a request. host is the Host header as it is sent (with the port when it is not the scheme's default); path is the absolute,
// unencoded path; query the unencoded pairs; headers every other header to sign, by lower-case name (x-amz-date and x-amz-content-sha256
// among them); payloadHash the hex SHA-256 of the body. at is the instant of the x-amz-date header.
func derive(c credentials, method, host, path string, query [][2]string, headers map[string]string, payloadHash string, at time.Time) derivation {
	all := map[string]string{"host": host}
	for k, v := range headers {
		all[strings.ToLower(k)] = v
	}
	names := make([]string, 0, len(all))
	for k := range all {
		names = append(names, k)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, k := range names {
		canonicalHeaders.WriteString(k + ":" + trimValue(all[k]) + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{method, encodePath(path), encodeQuery(query), canonicalHeaders.String(), signed, payloadHash}, "\n")
	day := at.UTC().Format("20060102")
	scope := day + "/" + c.region + "/" + service + "/aws4_request"
	toSign := strings.Join([]string{algorithm, at.UTC().Format(dateLayout), scope, hashHex(canonical)}, "\n")
	key := hmacSHA256([]byte("AWS4"+c.secret), day)
	key = hmacSHA256(key, c.region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	return derivation{CanonicalRequest: canonical, SignedHeaders: signed, StringToSign: toSign, Signature: hex.EncodeToString(hmacSHA256(key, toSign))}
}

// authorization is the value of the Authorization header for a derivation.
func authorization(c credentials, d derivation, at time.Time) string {
	scope := at.UTC().Format("20060102") + "/" + c.region + "/" + service + "/aws4_request"
	return algorithm + " Credential=" + c.access + "/" + scope + ", SignedHeaders=" + d.SignedHeaders + ", Signature=" + d.Signature
}
