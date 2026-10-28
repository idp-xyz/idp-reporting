package reporting_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	r "idp.local/reporting/reporting"
	"testing"
)

// The digest of a row (IssueIntent.RowSHA256, docs/API.md) is the SHA-256, in lowercase hexadecimal, of
// the row's strings as one JSON array with no white space, escaped as Go's encoding/json does. A product
// that is not written in Go implements it from the encoding below; these vectors, which docs/API.md
// repeats, are what it must reproduce. The encodings were also produced by an independent implementation
// (Python's json with the same escapes) and give the same digests.
func TestRowDigestEncodingAndVectors(t *testing.T) {
	for name, c := range map[string]struct {
		row      []string
		encoding string // the exact bytes that are hashed
		digest   string
	}{
		"plain ASCII": {
			[]string{"customer_a", "9007199254740893.00", "CNY"},
			`["customer_a","9007199254740893.00","CNY"]`,
			"aa1b2c6c29e265b6b34c10d929b210a1f2899afecb0c9d690d0d0e962dc0329d",
		},
		"Chinese, <&> and U+2028 U+2029": {
			[]string{"甲方<&>", "a\u2028b\u2029c", "1.00"},
			`["甲方\u003c\u0026\u003e","a\u2028b\u2029c","1.00"]`,
			"cf9dc98dd84f895258a57dd2714aa7231639c9d401c957c21a3788e8b0b8869f",
		},
		"quotes, controls and an invalid byte": {
			[]string{"a\"b\\c\n\t\x01\b\f\x7f\xff", ""},
			"[\"a\\\"b\\\\c\\n\\t\\u0001\\b\\f\x7f\\ufffd\",\"\"]",
			"aa997ec4fc9d759902afb618c37b68720017938ac533a317c1061df3f9f64212",
		},
		"no cells":       {[]string{}, `[]`, "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"},
		"one empty cell": {[]string{""}, `[""]`, "055539df4a0b804c58caf46c0cd2941af10d64c1395ddd8e50b5f55d945841e6"},
	} {
		t.Run(name, func(t *testing.T) {
			b, e := json.Marshal(c.row)
			if e != nil || string(b) != c.encoding {
				t.Fatalf("the encoding is %q (%v), the documented one %q", b, e, c.encoding)
			}
			sum := sha256.Sum256([]byte(c.encoding))
			if hex.EncodeToString(sum[:]) != c.digest {
				t.Fatalf("the documented encoding hashes to %x, not the documented digest %s", sum, c.digest)
			}
			if got := r.Digest(c.row); got != c.digest {
				t.Fatalf("Digest(row) is %s, the documented digest is %s", got, c.digest)
			}
		})
	}
}
