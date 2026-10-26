package reporting_test

import (
	r "idp.local/reporting/reporting"
	"strings"
	"testing"
)

func TestStrictJSONRejectsAuthorityNullDuplicatesAndExtraRoots(t *testing.T) {
	for _, b := range []string{`null`, `{}`, `{"tenant":"evil"}`, `{"schema_version":"1.0","schema_version":"2.0"}`, `{"page":{"limit":1,"limit":2}}`, `{"PAGE":{"limit":1}}`, `{"page":null}`, `{"page":{"limit":1.2}}`, `{} {}`, strings.Repeat("[", 34) + strings.Repeat("]", 34)} {
		var q r.QuerySpec
		e := r.DecodeStrict(strings.NewReader(b), &q)
		if b == `{}` {
			if e != nil {
				t.Fatal(e)
			}
			continue
		}
		if e == nil {
			t.Fatal("accepted", b)
		}
	}
}
