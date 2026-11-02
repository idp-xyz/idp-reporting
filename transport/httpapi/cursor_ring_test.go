package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/transport/httpapi"
)

// S04-H H1 at the HTTP level: the pagination of a snapshot continues across a restart that rotates the cursor key, with a real cursor taken
// before the restart and used after it; the ring is what keeps it valid and its absence is what ends it.
func TestPaginationContinuesAcrossARestartThatRotatesTheCursorKey(t *testing.T) {
	k1, k2, k3 := []byte("restart-ring-key-ONE-0123456789ab"), []byte("restart-ring-key-TWO-0123456789ab"), []byte("restart-ring-key-THREE-01234567ab")
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	repo := memory.New(time.Now)
	// start is one server process: the same store and source, the given ring.
	start := func(ring ...[]byte) (*r.Service, http.Handler) {
		t.Helper()
		s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: a}}, CursorKeys: ring})
		if e != nil {
			t.Fatal(e)
		}
		auth, e := httpapi.DemoBearer(secret, p)
		if e != nil {
			t.Fatal(e)
		}
		h, e := httpapi.Handler(s, auth)
		if e != nil {
			t.Fatal(e)
		}
		return s, h
	}
	s, h := start(k1)
	b, _ := json.Marshal(contractsim.FinancialQuery())
	w := request(h, "POST", "/api/reporting/v1/snapshot-jobs", b, true)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job r.JobView
	if e := json.Unmarshal(w.Body.Bytes(), &job); e != nil {
		t.Fatal(e)
	}
	if ran, e := s.RunOne(context.Background()); !ran || e != nil {
		t.Fatal(ran, e)
	}
	w = request(h, "GET", "/api/reporting/v1/jobs/"+job.ID, nil, true)
	if e := json.Unmarshal(w.Body.Bytes(), &job); e != nil || job.SnapshotID == "" {
		t.Fatal(e, w.Body.String())
	}
	rows := "/api/reporting/v1/snapshots/" + job.SnapshotID + "/rows?limit=1"
	page := func(h http.Handler, cursor string) (int, string, string) {
		t.Helper()
		path := rows
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		w := request(h, "GET", path, nil, true)
		var p r.RowsPage
		_ = json.Unmarshal(w.Body.Bytes(), &p)
		return w.Code, p.NextCursor, w.Body.String()
	}
	code, first, body := page(h, "")
	if code != 200 || first == "" {
		t.Fatal(code, body)
	}
	// The restart: K2 is now the newest key and K1 is kept. The cursor taken before is used after, and the page it gives is the second.
	_, rotated := start(k2, k1)
	code, _, body = page(rotated, first)
	if code != 200 {
		t.Fatalf("the cursor from before the restart is refused: %d %s", code, body)
	}
	// A cursor issued after the restart is signed by K2: it survives the next rotation that keeps K2, and not one that drops it.
	var second string
	_, second, _ = page(rotated, "")
	if second == "" || second == first {
		t.Fatal("the rotated service did not sign with its newest key")
	}
	_, next := start(k3, k2)
	if code, _, body = page(next, second); code != 200 {
		t.Fatalf("a cursor of K2 is refused by [K3,K2]: %d %s", code, body)
	}
	// Dropping the key ends it: 400 INVALID_REQUEST, exactly as for any cursor that was never valid, and the text carries no key.
	for name, ring := range map[string][][]byte{"[K2], which dropped K1": {k2}, "[K3], which dropped both": {k3}} {
		_, h := start(ring...)
		code, _, body = page(h, first)
		if code != 400 || body != "{\"error\":\"INVALID_REQUEST\"}\n" {
			t.Errorf("the cursor of K1 under %s: %d %q", name, code, body)
		}
		for _, k := range [][]byte{k1, k2, k3} {
			if strings.Contains(body, string(k)) {
				t.Errorf("the answer carries a key: %q", body)
			}
		}
	}
}
