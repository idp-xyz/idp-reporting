package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/storage/objects"
	"idp.local/reporting/transport/httpapi"
)

// What a caller sees when an artifact's file is in an object store (S04-E, docs/S04E_SCOPE.md E4, docs/API.md): the same views and the same
// download as before, a 503 with Retry-After: 10 when the store cannot be reached, a 409 INTEGRITY_FAILURE when the object is not the row's,
// and nothing of the store in any answer.
func TestObjectStoreDownloadsOverHTTP(t *testing.T) {
	ctx := context.Background()
	p := contractsim.DemoPrincipal()
	finance := contractsim.Financial()
	finance.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	raw := objects.NewMemory(nil)
	store := &flakyStore{ObjectStore: raw}
	service, e := r.NewService(r.Config{Repository: memory.New(time.Now), Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: finance}}, CursorKey: []byte(secret),
		Writer: xlsx.Writer{}, Objects: store})
	if e != nil {
		t.Fatal(e)
	}
	auth, _ := httpapi.DemoBearer(secret, p)
	h, e := httpapi.Handler(service, auth)
	if e != nil {
		t.Fatal(e)
	}
	const base = "/api/reporting/v1"
	snap, e := service.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	snap, _ = service.Job(ctx, p, snap.ID)
	ex, e := service.SubmitExport(ctx, p, snap.SnapshotID, "export")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = service.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	ex, _ = service.Job(ctx, p, ex.ID)
	path := base + "/artifacts/" + ex.ArtifactID + "/content"
	key := r.ObjectKey(ex.ArtifactID)
	// The download is what it always was: the file, its length and its digest.
	w := request(h, "GET", path, nil, true)
	if w.Code != 200 || w.Header().Get("X-Content-SHA256") == "" || w.Header().Get("Content-Length") == "" || !strings.HasPrefix(w.Body.String(), "PK") || w.Header().Get("Retry-After") != "" {
		t.Fatal(w.Code, w.Header())
	}
	digest := w.Header().Get("X-Content-SHA256")
	original := w.Body.Bytes()
	// The views are the same: the job names its artifact, and the answer carries no word about where the file lives.
	jobBody := request(h, "GET", base+"/jobs/"+ex.ID, nil, true).Body.String()
	capabilities := request(h, "GET", base+"/capabilities", nil, true).Body.String()
	for _, body := range []string{jobBody, capabilities} {
		for _, word := range []string{"object", "bucket", "s3", key, "minio"} {
			if strings.Contains(strings.ToLower(body), strings.ToLower(word)) {
				t.Errorf("an answer mentions %q: %s", word, body)
			}
		}
	}
	if !strings.Contains(jobBody, `"artifact_id":"`+ex.ArtifactID+`"`) || !strings.Contains(jobBody, `"state":"succeeded"`) {
		t.Fatal(jobBody)
	}
	// The store unreachable: 503, a Retry-After of 10 seconds, and a body that is the code and nothing else.
	store.down = true
	w = request(h, "GET", path, nil, true)
	var body map[string]any
	if w.Code != 503 || w.Header().Get("Retry-After") != "10" || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body) != 1 || body["error"] != "AUTHORITY_UNAVAILABLE" {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	for _, leak := range []string{key, "503", "object", "bucket", "10.1.2.3", "secret"} {
		if strings.Contains(strings.ToLower(w.Body.String()), strings.ToLower(leak)) {
			t.Errorf("the 503 carries %q: %s", leak, w.Body.String())
		}
	}
	// A poll of the job still answers, store or no store.
	if w = request(h, "GET", base+"/jobs/"+ex.ID, nil, true); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"succeeded"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	// An error the store invented is the same 503, with nothing of it.
	store.down, store.err = false, context.DeadlineExceeded
	if w = request(h, "GET", path, nil, true); w.Code != 503 || w.Header().Get("Retry-After") != "10" || strings.Contains(w.Body.String(), "deadline") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	store.err = nil
	// The object is not the row's: 409, never the file, and nothing of the store either.
	other := []byte("PK another file entirely")
	if e = raw.Put(ctx, key, other, digestOf(other)); e != nil {
		t.Fatal(e)
	}
	w = request(h, "GET", path, nil, true)
	if w.Code != 409 || w.Header().Get("Retry-After") != "" || strings.TrimSpace(w.Body.String()) != `{"error":"INTEGRITY_FAILURE"}` || strings.Contains(w.Body.String(), "another file") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	raw.Remove(key)
	if w = request(h, "GET", path, nil, true); w.Code != 409 || strings.TrimSpace(w.Body.String()) != `{"error":"INTEGRITY_FAILURE"}` {
		t.Fatal("a missing object", w.Code, w.Body.String())
	}
	if digestOf(original) != digest {
		t.Fatal("the download's digest is not the file's")
	}
	if e = raw.Put(ctx, key, original, digest); e != nil {
		t.Fatal(e)
	}
	if w = request(h, "GET", path, nil, true); w.Code != 200 || w.Body.String() != string(original) {
		t.Fatal("restored", w.Code)
	}
	// The source product's own outage is the same status without the wait the object store names: nothing about it changed.
	finance.SetUnavailable(true)
	if w = request(h, "GET", path, nil, true); w.Code != 503 || w.Header().Get("Retry-After") != "" {
		t.Fatal("a source outage", w.Code, w.Header())
	}
}

func digestOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

type flakyStore struct {
	r.ObjectStore
	down bool
	err  error
}

func (f *flakyStore) Get(ctx context.Context, key, sum string, n int) ([]byte, error) {
	if f.down {
		return nil, &r.ObjectStoreError{Status: 503}
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.ObjectStore.Get(ctx, key, sum, n)
}
