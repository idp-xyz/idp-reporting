package reporting_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
	"idp.local/reporting/storage/objects"
)

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestObjectKeysAreTheKeysOfCanonicalIDsAndNothingElse(t *testing.T) {
	id := r.NewID()
	if r.ObjectKey(id) != "artifacts/"+id {
		t.Fatal(r.ObjectKey(id))
	}
	if got, ok := r.ObjectID("artifacts/" + id); !ok || got != id {
		t.Fatal(got, ok)
	}
	for _, key := range []string{"", "artifacts/", "artifacts/" + strings.ToUpper(id), "artifacts/" + id + "x", "artifacts/" + id[1:], "artifacts/sub/" + id, "other/" + id, id, "/artifacts/" + id, "artifacts/" + id + "/", "artifacts/../" + id} {
		if got, ok := r.ObjectID(key); ok {
			t.Errorf("%q is taken for the key of artifact %q", key, got)
		}
	}
}

func TestAnArtifactCarriesItsFileInTheRowOrInTheObjectStoreNeverBothNorNeither(t *testing.T) {
	content := []byte("PK a workbook")
	id := r.NewID()
	inline := r.Artifact{ID: id, Content: content, SHA256: sum(content)}
	object := r.Artifact{ID: id, Object: r.ObjectKey(id), Size: len(content), SHA256: sum(content)}
	if !r.ValidArtifactBody(inline) || !r.ValidArtifactBody(object) {
		t.Fatal("a valid artifact is refused")
	}
	mutate := func(a r.Artifact, f func(*r.Artifact)) r.Artifact { f(&a); return a }
	for name, a := range map[string]r.Artifact{
		"neither":                          mutate(inline, func(a *r.Artifact) { a.Content = nil }),
		"both":                             mutate(object, func(a *r.Artifact) { a.Content = content }),
		"inline with a length":             mutate(inline, func(a *r.Artifact) { a.Size = len(content) }),
		"inline with another digest":       mutate(inline, func(a *r.Artifact) { a.SHA256 = sum([]byte("x")) }),
		"inline over the bound":            mutate(inline, func(a *r.Artifact) { a.Content = make([]byte, r.MaxArtifactBytes+1); a.SHA256 = sum(a.Content) }),
		"object of another artifact":       mutate(object, func(a *r.Artifact) { a.Object = r.ObjectKey(r.NewID()) }),
		"object outside the prefix":        mutate(object, func(a *r.Artifact) { a.Object = id }),
		"object with no length":            mutate(object, func(a *r.Artifact) { a.Size = 0 }),
		"object with a negative length":    mutate(object, func(a *r.Artifact) { a.Size = -1 }),
		"object over the bound":            mutate(object, func(a *r.Artifact) { a.Size = r.MaxArtifactBytes + 1 }),
		"object with no digest":            mutate(object, func(a *r.Artifact) { a.SHA256 = "" }),
		"object with an upper-case digest": mutate(object, func(a *r.Artifact) { a.SHA256 = strings.ToUpper(a.SHA256) }),
	} {
		if r.ValidArtifactBody(a) {
			t.Errorf("%s: accepted", name)
		}
	}
	if !r.ValidArtifactBody(mutate(object, func(a *r.Artifact) { a.Size = r.MaxArtifactBytes })) {
		t.Error("an object of the largest size is refused")
	}
}

func TestTheObjectStoresErrorIsUnavailableAndSaysNothingButAStatus(t *testing.T) {
	e := fmt.Errorf("while reading: %w", &r.ObjectStoreError{Status: 503})
	if !errors.Is(e, r.ErrUnavailable) || !r.IsObjectStoreError(e) || r.IsObjectStoreError(r.ErrUnavailable) || r.IsObjectStoreError(errors.New("x")) {
		t.Fatal("classification")
	}
	if (&r.ObjectStoreError{}).Error() != "object store unavailable" || (&r.ObjectStoreError{Status: 403}).Error() != "object store unavailable (status 403)" {
		t.Fatal("text")
	}
}

// ledger is the order in which the store, the repository and the product were called.
type ledger struct {
	mu     sync.Mutex
	events []string
}

func (l *ledger) add(s string) { l.mu.Lock(); l.events = append(l.events, s); l.mu.Unlock() }

type recordingStore struct {
	r.ObjectStore
	l    *ledger
	puts []struct {
		key, sum string
		n        int
	}
	fail bool
}

func (s *recordingStore) Put(ctx context.Context, key string, content []byte, sha string) error {
	s.l.add("put")
	s.puts = append(s.puts, struct {
		key, sum string
		n        int
	}{key, sha, len(content)})
	if s.fail {
		return errors.New("dial tcp 10.1.2.3:9000: connection refused, bucket secret-bucket, key " + key)
	}
	return s.ObjectStore.Put(ctx, key, content, sha)
}

type recordingRepo struct {
	r.Repository
	l   *ledger
	pub []r.Publication
}

func (p *recordingRepo) Publish(ctx context.Context, c r.Claim, pub r.Publication) error {
	p.l.add("publish")
	p.pub = append(p.pub, pub)
	return p.Repository.Publish(ctx, c, pub)
}

// recordingProduct is the source product, noting when it is asked to confirm a file.
type recordingProduct struct {
	*contractsim.Simulator
	l *ledger
}

func (p recordingProduct) ConfirmIssue(ctx context.Context, sc r.SourceContext, c r.IssueConfirmation) (r.IssueReceipt, error) {
	p.l.add("confirm")
	return p.Simulator.ConfirmIssue(ctx, sc, c)
}

type pdfIssuer struct{}

func (pdfIssuer) Check(context.Context, r.Job, r.Snapshot) error { return nil }
func (pdfIssuer) Issue(_ context.Context, req r.IssueRequest) (r.IssueResult, error) {
	return r.IssueResult{PDF: []byte("%PDF-1.7\n" + req.Stamp + "\n%%EOF"), Pages: 1}, nil
}

type pdfWriter struct{}

func (pdfWriter) Write(context.Context, r.Snapshot) ([]byte, error) { return []byte("%PDF-1.7 x"), nil }

func objectService(t *testing.T, withStore bool) (*r.Service, r.Principal, *recordingStore, *recordingRepo, *ledger) {
	t.Helper()
	l := &ledger{}
	p := contractsim.DemoPrincipal()
	a := contractsim.Financial()
	a.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	store := &recordingStore{ObjectStore: objects.NewMemory(nil), l: l}
	repo := &recordingRepo{Repository: memory.New(time.Now), l: l}
	cfg := r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: recordingProduct{a, l}}}, CursorKey: []byte("01234567890123456789012345678901"),
		Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": pdfWriter{}}, Issuer: pdfIssuer{}}
	if withStore {
		cfg.Objects = store
	}
	s, e := r.NewService(cfg)
	if e != nil {
		t.Fatal(e)
	}
	return s, p, store, repo, l
}

func runJob(t *testing.T, s *r.Service) {
	t.Helper()
	if _, e := s.RunOne(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestTheWorkerPutsTheObjectThenPublishesARowWithoutTheContent(t *testing.T) {
	ctx := context.Background()
	s, p, store, repo, l := objectService(t, true)
	snap, e := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	if e != nil {
		t.Fatal(e)
	}
	runJob(t, s)
	snap, _ = s.Job(ctx, p, snap.ID)
	ex, e := s.SubmitExport(ctx, p, snap.SnapshotID, "export")
	if e != nil {
		t.Fatal(e)
	}
	runJob(t, s)
	ex, _ = s.Job(ctx, p, ex.ID)
	if len(store.puts) != 1 || len(repo.pub) != 2 {
		t.Fatalf("%d puts, %d publications", len(store.puts), len(repo.pub))
	}
	art := repo.pub[1].Artifact
	put := store.puts[0]
	if art == nil || art.Object != r.ObjectKey(art.ID) || art.Content != nil || art.Size != put.n || art.SHA256 != put.sum || put.key != art.Object || art.ID != ex.ArtifactID {
		t.Fatalf("publication %+v, put %+v", art, put)
	}
	// The snapshot is published, then the export's object is put, then the export's row is published.
	want := []string{"publish", "put", "publish"}
	if fmt.Sprint(l.events) != fmt.Sprint(want) {
		t.Fatalf("calls in order: %v, want %v", l.events, want)
	}
	// Without an object store the same job publishes the content in the row, as before.
	s2, p2, store2, repo2, _ := objectService(t, false)
	sn, _ := s2.SubmitSnapshot(ctx, p2, contractsim.FinancialQuery(), "snap")
	runJob(t, s2)
	sn, _ = s2.Job(ctx, p2, sn.ID)
	if _, e = s2.SubmitExport(ctx, p2, sn.SnapshotID, "export"); e != nil {
		t.Fatal(e)
	}
	runJob(t, s2)
	if a := repo2.pub[1].Artifact; a.Object != "" || a.Size != 0 || len(a.Content) == 0 || len(store2.puts) != 0 {
		t.Fatalf("%+v", a)
	}
}

func TestAnIssuedFileIsPutBeforeTheProductIsToldItExists(t *testing.T) {
	ctx := context.Background()
	s, p, store, repo, l := objectService(t, true)
	snap, _ := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	runJob(t, s)
	snap, _ = s.Job(ctx, p, snap.ID)
	in := r.IssueInput{TemplateID: strings.Repeat("a", 32), TemplateVersion: 1, Template: []byte(`{"a":1}`), SnapshotID: snap.SnapshotID}
	if _, e := s.SubmitIssue(ctx, p, in, "issue"); e != nil {
		t.Fatal(e)
	}
	runJob(t, s)
	if want := []string{"publish", "put", "confirm", "publish"}; fmt.Sprint(l.events) != fmt.Sprint(want) {
		t.Fatalf("calls in order: %v, want %v: the file is put, then the product is told it exists, then the row is published", l.events, want)
	}
	art := repo.pub[1].Artifact
	if art.Object != r.ObjectKey(art.ID) || art.Content != nil || art.Issue == "" || len(store.puts) != 1 || repo.pub[1].Issue.SHA256 != art.SHA256 {
		t.Fatalf("%+v", art)
	}
}

func TestAFailedPutIsExecutionFailedAndCarriesNothingOfTheStore(t *testing.T) {
	ctx := context.Background()
	s, p, store, repo, _ := objectService(t, true)
	snap, _ := s.SubmitSnapshot(ctx, p, contractsim.FinancialQuery(), "snap")
	runJob(t, s)
	snap, _ = s.Job(ctx, p, snap.ID)
	store.fail = true
	ex, _ := s.SubmitExport(ctx, p, snap.SnapshotID, "export")
	ran, e := s.RunOne(ctx)
	if !ran || e == nil {
		t.Fatal(ran, e)
	}
	for _, leak := range []string{"10.1.2.3", "9000", "secret-bucket", "artifacts/", "dial", "refused"} {
		if strings.Contains(e.Error(), leak) {
			t.Errorf("the error of a job carries %q: %q", leak, e)
		}
	}
	view, _ := s.Job(ctx, p, ex.ID)
	if view.State != "failed" || view.ErrorCode != "EXECUTION_FAILED" || len(repo.pub) != 1 {
		t.Fatalf("%+v, %d publications", view, len(repo.pub))
	}
}
