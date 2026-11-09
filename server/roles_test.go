package server

import (
	"errors"
	"strings"
	"testing"
)

// S12-A (ADR 0018): a binding may name its roles; the session shows them; an empty list or an unknown name is refused (the file's rules, the
// profile's), and a reload keeps the previous bindings when it is.
func TestBindingsCarryRoles(t *testing.T) {
	list, err := parseBindings([]byte(`[{"subject":"a","client_id":"c","tenant":"t","integrations":["p"],"roles":["viewer","publisher"]},{"subject":"b","client_id":"c","tenant":"t","integrations":["p"]}]`))
	if err != nil || len(list) != 2 || strings.Join(list[0].Roles, ",") != "viewer,publisher" || list[1].Roles != nil {
		t.Fatalf("%+v %v", list, err)
	}
	if _, err := parseBindings([]byte(`[{"subject":"a","client_id":"c","tenant":"t","integrations":["p"],"roles":[]}]`)); err == nil || !strings.Contains(err.Error(), "at least one role") {
		t.Fatalf("an empty list: %v", err)
	}
	w := newWorld(t)
	s := startWorld(t, w)
	token := mint(t, w.keys[0], claims{})
	session := func() string {
		t.Helper()
		code, _, body := s.do(t, "GET", "/api/reporting/v1/session", token, nil)
		if code != 200 {
			t.Fatal(code, string(body))
		}
		return string(body)
	}
	if !strings.Contains(session(), `"roles":["viewer","designer","publisher","issuer"]`) {
		t.Fatal("the default rights", session())
	}
	replaceFile(t, w.binds, []byte(`[{"subject":"`+testSubject+`","client_id":"`+testClient+`","tenant":"`+testTenant+`","integrations":["`+w.finance+`"],"roles":["viewer"]}]`), 0o644)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(session(), `"roles":["viewer"]`) {
		t.Fatal("the bound roles", session())
	}
	// An unknown role is the profile's refusal: the previous bindings stay.
	replaceFile(t, w.binds, []byte(`[{"subject":"`+testSubject+`","client_id":"`+testClient+`","tenant":"`+testTenant+`","integrations":["`+w.finance+`"],"roles":["owner"]}]`), 0o644)
	var re *ReloadError
	if err := s.Reload(); !errors.As(err, &re) || re.Code != CodeIssuerConfigRefused {
		t.Fatalf("an unknown role: %v", err)
	}
	if !strings.Contains(session(), `"roles":["viewer"]`) {
		t.Fatal("the previous bindings", session())
	}
}
