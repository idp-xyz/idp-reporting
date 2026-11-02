package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// The object storage contract of S04-E (docs/S04E_SCOPE.md E2-E4, E6; docs/ADR_0007_OBJECT_STORAGE.md), run on both stores and on both object
// stores: the memory store and the durable store, objects.Memory and objects.S3 against a throwaway S3-compatible server. A world is a service, a store and
// an object store, with a clock that lets time pass for all three (as the retention harness does) and an object store that can be made to
// fail the way a real one fails.

// faultyStore is the object store the service and the repository are given: the real one behind faults a test injects, and a record of the
// calls that matter. Tampering with what the store holds goes to the real one.
type faultyStore struct {
	r.ObjectStore
	mu       sync.Mutex
	down     map[string]bool
	onDelete func(key string) // runs before a delete is passed on
	puts     []string
	deletes  []string
}

func (f *faultyStore) fail(ops ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = map[string]bool{}
	for _, op := range ops {
		f.down[op] = true
	}
}
func (f *faultyStore) check(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down[op] {
		return &r.ObjectStoreError{Status: 503}
	}
	return nil
}
func (f *faultyStore) Put(ctx context.Context, key string, content []byte, sum string) error {
	if e := f.check("put"); e != nil {
		return e
	}
	f.mu.Lock()
	f.puts = append(f.puts, key)
	f.mu.Unlock()
	return f.ObjectStore.Put(ctx, key, content, sum)
}
func (f *faultyStore) Get(ctx context.Context, key, sum string, length int) ([]byte, error) {
	if e := f.check("get"); e != nil {
		return nil, e
	}
	return f.ObjectStore.Get(ctx, key, sum, length)
}
func (f *faultyStore) Delete(ctx context.Context, key string) error {
	f.mu.Lock()
	hook := f.onDelete
	f.mu.Unlock()
	if hook != nil {
		hook(key)
	}
	if e := f.check("delete"); e != nil {
		return e
	}
	f.mu.Lock()
	f.deletes = append(f.deletes, key)
	f.mu.Unlock()
	return f.ObjectStore.Delete(ctx, key)
}
func (f *faultyStore) List(ctx context.Context, prefix, after string, limit int) ([]r.Object, string, error) {
	if e := f.check("list"); e != nil {
		return nil, "", e
	}
	return f.ObjectStore.List(ctx, prefix, after, limit)
}

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// objWorld is a retention harness whose artifacts live in an object store.
type objWorld struct {
	*retention
	raw      r.ObjectStore // the object store itself
	store    *faultyStore  // what the service and the repository are given
	product  func(jobID string) (state string, confirmedSHA string, found bool)
	rowsOnly func(o r.SweepOptions) r.SweepResult                   // the operator's sweep without the object store flags
	over     func(repo r.Repository, objs r.ObjectStore) *r.Service // a service of this world over another repository, with or without the store
	legacy   func() *r.Service                                      // a service without the object store over the same rows: it keeps content inline
	reopen   func() r.Repository                                    // the same rows through a repository that was given no object store
}

// keys lists the objects the bucket holds under artifacts/, as the real store reports them.
func (w *objWorld) keys() []string {
	w.t.Helper()
	var out []string
	after := ""
	for {
		page, next, e := w.raw.List(context.Background(), r.ObjectPrefix, after, 1000)
		if e != nil {
			w.t.Fatal(e)
		}
		for _, o := range page {
			out = append(out, o.Key)
		}
		if next == "" {
			return out
		}
		after = next
	}
}

func (w *objWorld) artifactRow(id string) r.Artifact {
	w.t.Helper()
	a, e := w.repo.Artifact(context.Background(), w.p.Identity, id)
	if e != nil {
		w.t.Fatal(e)
	}
	return a
}

// aged is how long the rows and the objects of a world are made to age to be past the grace by the time the sweep looks (the read lives ten
// minutes): a little more than readLife plus the grace.
const objGrace = time.Hour

func (w *objWorld) pastGrace() { w.age(readLife + objGrace + time.Minute) }

func (w *objWorld) sweepObjects(o r.SweepOptions) r.SweepResult { return w.sweep(o) }

// emptyBucket removes what an earlier test left under artifacts/ in a shared bucket: the rows of the fixture are reset between tests, and the
// objects would then be orphans of this one.
func emptyBucket(t *testing.T, s r.ObjectStore) {
	t.Helper()
	ctx := context.Background()
	for {
		page, _, e := s.List(ctx, r.ObjectPrefix, "", 1000)
		if e != nil {
			t.Fatal(e)
		}
		if len(page) == 0 {
			return
		}
		for _, o := range page {
			if e = s.Delete(ctx, o.Key); e != nil {
				t.Fatal(e)
			}
		}
	}
}

func newObjectsMemory(t *testing.T) *objWorld {
	t.Helper()
	clock := &retentionClock{t: time.Date(2026, 10, 29, 6, 0, 0, 123456789, time.UTC)}
	p := contractsim.DemoPrincipal()
	finance := contractsim.Financial()
	finance.SetClock(clock.Now)
	finance.Grant(contractsim.DemoOwner(p, contractsim.FinanceIntegration), contractsim.DefaultGrant())
	raw := objects.NewMemory(clock.Now)
	store := &faultyStore{ObjectStore: raw}
	repo := memory.New(clock.Now).UseObjects(store)
	issuer := &stampIssuer{}
	service := func(repo r.Repository, objs r.ObjectStore) func(now func() time.Time) *r.Service {
		return func(now func() time.Time) *r.Service {
			s, e := r.NewService(r.Config{Repository: repo, Connections: []r.Connection{{Tenant: p.Tenant, Integration: contractsim.FinanceIntegration, SourceTenant: "tenant_a", Adapter: finance}}, CursorKey: []byte("01234567890123456789012345678901"),
				Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": fakePDFWriter{}}, Issuer: issuer, Now: now, Objects: objs})
			if e != nil {
				t.Fatal(e)
			}
			return s
		}
	}
	h := &retention{t: t, repo: repo, s: service(repo, store)(clock.Now), p: p, issuer: issuer, age: clock.Advance, service: service(repo, store)}
	return &objWorld{retention: h, raw: raw, store: store,
		product: func(id string) (string, string, bool) {
			rec, ok := finance.IssueRecord(id)
			return rec.State, rec.SHA256, ok
		},
		rowsOnly: func(o r.SweepOptions) r.SweepResult {
			repo.UseObjects(nil)
			defer repo.UseObjects(store)
			res, e := repo.Sweep(context.Background(), o)
			if e != nil {
				t.Fatal(e)
			}
			return res
		},
		over:   func(repo r.Repository, objs r.ObjectStore) *r.Service { return service(repo, objs)(clock.Now) },
		legacy: func() *r.Service { return service(repo, nil)(clock.Now) },
		reopen: func() r.Repository { return repo },
	}
}

func TestMemoryObjectContract(t *testing.T) { runObjectContract(t, newObjectsMemory) }

func runObjectContract(t *testing.T, newWorld func(*testing.T) *objWorld) {
	ctx := context.Background()
	t.Run("files_are_objects_and_rows_carry_the_metadata", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		export := w.export(snap.SnapshotID, "export")
		issue := w.issue(snap.SnapshotID, "issue")
		// Only the files are objects: one per artifact, nothing for the snapshot or the issue record.
		want := map[string]bool{r.ObjectKey(export.ArtifactID): true, r.ObjectKey(issue.ArtifactID): true}
		got := w.keys()
		if len(got) != 2 || !want[got[0]] || !want[got[1]] {
			t.Fatalf("the bucket holds %v, want the objects of %v", got, want)
		}
		for name, c := range map[string]struct{ id, magic string }{"workbook": {export.ArtifactID, "PK"}, "issued PDF": {issue.ArtifactID, "%PDF-"}} {
			row := w.artifactRow(c.id)
			if row.Object != r.ObjectKey(row.ID) || row.Content != nil || row.Size <= 0 || !strings.HasPrefix(row.Object, "artifacts/") {
				t.Fatalf("the row of the %s is %+v", name, row)
			}
			a, e := w.s.Download(ctx, w.p, c.id)
			if e != nil || len(a.Content) != row.Size || sha(a.Content) != row.SHA256 || !strings.HasPrefix(string(a.Content), c.magic) {
				t.Fatalf("the %s is not served as its row says: %v", name, e)
			}
			// What the store holds is what is served, and is what the row's digest says.
			raw, e := w.raw.Get(ctx, row.Object, row.SHA256, row.Size)
			if e != nil || string(raw) != string(a.Content) {
				t.Fatalf("the object of the %s is not the file served: %v", name, e)
			}
			if a.Object != row.Object || a.Size != row.Size || a.SHA256 != row.SHA256 {
				t.Fatalf("the download changed the row: %+v", a)
			}
		}
		// The issued PDF is the record's.
		rec, e := w.repo.Issue(ctx, w.p.Identity, issue.ID)
		if e != nil || rec.SHA256 != w.artifactRow(issue.ArtifactID).SHA256 {
			t.Fatal(rec.SHA256, e)
		}
	})
	t.Run("the_object_is_put_before_the_row_and_the_row_is_deleted_before_the_object", func(t *testing.T) {
		w := newWorld(t)
		// 1. At the moment the row is published the object is already there, for an export and for an issue.
		var published []string
		var missing []string
		watched := &publishWatcher{Repository: w.repo, before: func(p r.Publication) {
			if p.Artifact == nil {
				return
			}
			published = append(published, p.Artifact.ID)
			if _, e := w.raw.Get(ctx, p.Artifact.Object, p.Artifact.SHA256, p.Artifact.Size); e != nil {
				missing = append(missing, p.Artifact.ID)
			}
		}}
		s := w.over(watched, w.store)
		snap, e := s.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), "snap")
		if e != nil {
			t.Fatal(e)
		}
		runOne := func() {
			t.Helper()
			if _, e := s.RunOne(ctx); e != nil {
				t.Fatal(e)
			}
		}
		runOne()
		sv, _ := s.Job(ctx, w.p, snap.ID)
		ex, e := s.SubmitExport(ctx, w.p, sv.SnapshotID, "export")
		if e != nil {
			t.Fatal(e)
		}
		runOne()
		is, e := s.SubmitIssue(ctx, w.p, issueInput(sv.SnapshotID, 0), "issue")
		if e != nil {
			t.Fatal(e)
		}
		runOne()
		if len(published) != 2 || len(missing) != 0 {
			t.Fatalf("published %v, of which the object was not there at %v", published, missing)
		}
		if len(w.store.puts) != 2 {
			t.Fatalf("puts %v", w.store.puts)
		}
		_, _ = ex, is
		// 2. The sweep deletes the row and only then the object.
		w.pastGrace()
		var early []string
		w.store.onDelete = func(key string) {
			id, _ := r.ObjectID(key)
			if _, e := w.repo.Artifact(ctx, w.p.Identity, id); !errors.Is(e, r.ErrNotFound) {
				early = append(early, key)
			}
		}
		res := w.sweep(r.SweepOptions{Grace: objGrace})
		if res.Artifacts != 2 || res.Objects != 2 || len(w.store.deletes) != 2 || len(early) != 0 {
			t.Fatalf("%+v; deletes %v; deleted before their rows: %v", res, w.store.deletes, early)
		}
	})
	t.Run("an_issue_is_put_before_the_product_is_told_the_file_exists", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		w.store.fail("put")
		j, e := w.s.SubmitIssue(ctx, w.p, issueInput(snap.SnapshotID, 0), "issue")
		if e != nil {
			t.Fatal(e)
		}
		ran, e := w.s.RunOne(ctx)
		if !ran || e == nil {
			t.Fatal("a job whose file could not be put did not fail", ran, e)
		}
		// The product reserved a number (it was asked to authorize the issue) and was never told a file exists; the job is voided.
		state, confirmed, found := w.product(j.ID)
		if !found || state != contractsim.IssueVoid || confirmed != "" {
			t.Fatalf("the product holds %q (confirmed file %q, found %v), want a voided reservation that never confirmed a file", state, confirmed, found)
		}
	})
	t.Run("a_put_that_fails_fails_the_job_and_publishes_nothing", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		w.store.fail("put")
		j, e := w.s.SubmitExport(ctx, w.p, snap.SnapshotID, "export")
		if e != nil {
			t.Fatal(e)
		}
		ran, e := w.s.RunOne(ctx)
		if !ran || e == nil {
			t.Fatal(ran, e)
		}
		// Nothing of the store is in the error, and the code is the job's own.
		if e.Error() != "the artifact could not be stored" || errors.Is(e, r.ErrUnavailable) {
			t.Fatalf("the failure of the put is %q", e)
		}
		view, e := w.s.Job(ctx, w.p, j.ID)
		if e != nil || view.State != "failed" || view.ErrorCode != "EXECUTION_FAILED" || view.ArtifactID != "" {
			t.Fatalf("%+v %v", view, e)
		}
		if got := w.keys(); len(got) != 0 {
			t.Fatalf("the bucket holds %v after a put that failed", got)
		}
		// An issue is the same, voided as any failure (S03-H): its code is its own once the product voided what it held.
		is, e := w.s.SubmitIssue(ctx, w.p, issueInput(snap.SnapshotID, 0), "issue")
		if e != nil {
			t.Fatal(e)
		}
		if ran, e = w.s.RunOne(ctx); !ran || e == nil || e.Error() != "the artifact could not be stored" {
			t.Fatal(ran, e)
		}
		iv, e := w.s.Job(ctx, w.p, is.ID)
		if e != nil || iv.State != "failed" || iv.ErrorCode != "EXECUTION_FAILED" || iv.ArtifactID != "" {
			t.Fatalf("%+v %v", iv, e)
		}
		if _, e := w.repo.Issue(ctx, w.p.Identity, is.ID); !errors.Is(e, r.ErrNotFound) {
			t.Fatalf("an issue record exists for a file that was not kept: %v", e)
		}
		// The store back, a new key: the same work succeeds.
		w.store.fail()
		w.export(snap.SnapshotID, "export again")
	})
	t.Run("a_failure_between_the_put_and_the_publication_leaves_an_orphan_the_reconciliation_removes_after_the_grace", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		injected := errors.New("injected between the put and the publication")
		s := w.over(&publishWatcher{Repository: w.repo, fail: injected}, w.store)
		j, e := s.SubmitExport(ctx, w.p, snap.SnapshotID, "export")
		if e != nil {
			t.Fatal(e)
		}
		if ran, e := s.RunOne(ctx); !ran || !errors.Is(e, injected) {
			t.Fatal(ran, e)
		}
		view, _ := w.s.Job(ctx, w.p, j.ID)
		keys := w.keys()
		if view.State != "failed" || len(keys) != 1 || view.ArtifactID != "" {
			t.Fatalf("job %+v; the bucket holds %v", view, keys)
		}
		id, ok := r.ObjectID(keys[0])
		if !ok {
			t.Fatal(keys)
		}
		// The row is absent: the object is an orphan.
		if _, e := w.repo.Artifact(ctx, w.p.Identity, id); !errors.Is(e, r.ErrNotFound) {
			t.Fatalf("a row exists for the orphan: %v", e)
		}
		// Young, it is kept (its row could be about to be published); a dry run counts it only once it is old.
		if res := w.sweep(r.SweepOptions{Grace: objGrace}); res.Orphans != 0 || len(w.keys()) != 1 {
			t.Fatalf("a young object was reconciled: %+v", res)
		}
		w.age(objGrace + 5*time.Minute)
		dry := w.sweep(r.SweepOptions{Grace: objGrace, DryRun: true})
		if dry.Orphans != 1 || dry.Objects != 0 || len(w.keys()) != 1 {
			t.Fatalf("the dry run: %+v, bucket %v", dry, w.keys())
		}
		res := w.sweep(r.SweepOptions{Grace: objGrace})
		if res.Orphans != 1 || res.Objects != 0 || res.Artifacts != 0 || len(w.keys()) != 0 {
			t.Fatalf("%+v, bucket %v", res, w.keys())
		}
	})
	t.Run("downloads_check_every_byte_against_the_row", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		ex := w.export(snap.SnapshotID, "export")
		row := w.artifactRow(ex.ArtifactID)
		key := row.Object
		original, e := w.raw.Get(ctx, key, row.SHA256, row.Size)
		if e != nil {
			t.Fatal(e)
		}
		put := func(b []byte) {
			t.Helper()
			if e := w.raw.Put(ctx, key, b, sha(b)); e != nil {
				t.Fatal(e)
			}
		}
		flipped := append([]byte(nil), original...)
		flipped[len(flipped)/2] ^= 0x01
		for name, damage := range map[string]func(){
			"the same length and another digest": func() { put(flipped) },
			"a shorter object":                   func() { put(original[:len(original)-1]) },
			"a longer object":                    func() { put(append(append([]byte(nil), original...), 'x')) },
			"an object of another artifact":      func() { put([]byte("PK a workbook of someone else")) },
			"a missing object":                   func() { _ = w.raw.Delete(ctx, key) },
		} {
			damage()
			a, e := w.s.Download(ctx, w.p, ex.ArtifactID)
			if !errors.Is(e, r.ErrIntegrity) || len(a.Content) != 0 || a.ID != "" {
				t.Errorf("%s: served (%d bytes, %v)", name, len(a.Content), e)
			} else if strings.Contains(e.Error(), key) || strings.Contains(e.Error(), ex.ArtifactID) || r.IsObjectStoreError(e) {
				t.Errorf("%s: the error names the key or the store: %q", name, e)
			}
			if _, e = w.s.Download(ctx, w.p, ex.ArtifactID); errors.Is(e, r.ErrNotFound) {
				t.Errorf("%s: the row vanished: %v", name, e)
			}
		}
		// Put back, it is served again, and it is the same bytes.
		put(original)
		a, e := w.s.Download(ctx, w.p, ex.ArtifactID)
		if e != nil || string(a.Content) != string(original) {
			t.Fatal("the restored file is not served", e)
		}
	})
	t.Run("a_lying_object_store_cannot_make_the_service_serve_a_wrong_file", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		ex := w.export(snap.SnapshotID, "export")
		row := w.artifactRow(ex.ArtifactID)
		original, _ := w.raw.Get(ctx, row.Object, row.SHA256, row.Size)
		for name, answer := range map[string][]byte{"another digest": append([]byte("X"), original[1:]...), "another length": original[:len(original)-3], "nothing": nil} {
			s := w.over(w.repo, lyingStore{ObjectStore: w.store, answer: answer})
			if a, e := s.Download(ctx, w.p, ex.ArtifactID); !errors.Is(e, r.ErrIntegrity) || len(a.Content) != 0 {
				t.Errorf("%s: a store that answers without an error was believed (%d bytes, %v)", name, len(a.Content), e)
			}
		}
		// A store that answers with an error of its own invention is not relayed either: it is the store being unavailable, with no text.
		s := w.over(w.repo, lyingStore{ObjectStore: w.store, err: errors.New("dial tcp 10.1.2.3:9000: secret-endpoint refused for key " + row.Object)})
		_, e := s.Download(ctx, w.p, ex.ArtifactID)
		if !errors.Is(e, r.ErrUnavailable) || !r.IsObjectStoreError(e) || strings.Contains(e.Error(), "10.1.2.3") || strings.Contains(e.Error(), row.Object) {
			t.Errorf("an unknown error of the store came out as %q", e)
		}
	})
	t.Run("an_unreachable_store_is_unavailable_and_the_job_view_does_not_depend_on_it", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		ex := w.export(snap.SnapshotID, "export")
		is := w.issue(snap.SnapshotID, "issue")
		w.store.fail("get", "put", "delete", "list")
		_, e := w.s.Download(ctx, w.p, ex.ArtifactID)
		var o *r.ObjectStoreError
		if !errors.Is(e, r.ErrUnavailable) || !errors.As(e, &o) || e.Error() != "object store unavailable (status 503)" {
			t.Fatalf("a download with the store down: %v", e)
		}
		// A poll reads rows only: it says what happened whether or not the store is up, and an issue's record and listing too.
		for _, id := range []string{ex.ID, is.ID} {
			if v, e := w.s.Job(ctx, w.p, id); e != nil || v.State != "succeeded" || v.ArtifactID == "" {
				t.Fatalf("the poll of %s with the store down: %+v %v", id, v, e)
			}
		}
		if list, e := w.s.Issues(ctx, w.p, 10); e != nil || len(list) != 1 || list[0].Number == "" {
			t.Fatalf("%+v %v", list, e)
		}
		w.store.fail()
		if a, e := w.s.Download(ctx, w.p, ex.ArtifactID); e != nil || len(a.Content) == 0 {
			t.Fatal("the store back, the file is served again", e)
		}
	})
	t.Run("the_sweep_deletes_the_objects_of_the_rows_it_deletes_and_counts_them", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		w.export(snap.SnapshotID, "export")
		issue := w.issue(snap.SnapshotID, "issue")
		// Valid: nothing goes, objects included.
		if res := w.sweep(r.SweepOptions{Grace: objGrace}); res.Artifacts+res.Objects+res.Orphans != 0 || len(w.keys()) != 2 {
			t.Fatalf("%+v", res)
		}
		w.age(readLife + objGrace - time.Minute)
		if res := w.sweep(r.SweepOptions{Grace: objGrace}); res.Artifacts+res.Objects+res.Orphans != 0 || len(w.keys()) != 2 {
			t.Fatal("inside the grace", res)
		}
		w.age(2 * time.Minute)
		dry := w.sweep(r.SweepOptions{Grace: objGrace, DryRun: true})
		if !dry.DryRun || dry.Artifacts != 2 || dry.Objects != 2 || dry.Orphans != 0 || dry.ObjectsLeft != 0 || len(w.keys()) != 2 || dry.Batches != 0 {
			t.Fatalf("the dry run: %+v, bucket %v", dry, w.keys())
		}
		res := w.sweep(r.SweepOptions{Grace: objGrace})
		if res.DryRun || res.Artifacts != 2 || res.Snapshots != 1 || res.Objects != 2 || res.Orphans != 0 || res.ObjectsLeft != 0 || len(w.keys()) != 0 {
			t.Fatalf("%+v, bucket %v", res, w.keys())
		}
		if !w.held("issue", issue.ID) {
			t.Fatal("the issue record is gone")
		}
		if again := w.sweep(r.SweepOptions{Grace: objGrace}); again.Artifacts+again.Snapshots+again.Objects+again.Orphans+again.ObjectsLeft != 0 {
			t.Fatal(again)
		}
	})
	t.Run("a_delete_that_fails_is_counted_and_the_reconciliation_finishes_the_work", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		w.export(snap.SnapshotID, "export")
		w.issue(snap.SnapshotID, "issue")
		w.pastGrace()
		w.store.fail("delete")
		res := w.sweep(r.SweepOptions{Grace: objGrace})
		if res.Artifacts != 2 || res.Objects != 0 || res.ObjectsLeft != 2 || len(w.keys()) != 2 {
			t.Fatalf("%+v, bucket %v", res, w.keys())
		}
		// The rows are gone and the objects are orphans: a download of either is a swept artifact's 404 and no file.
		for _, k := range w.keys() {
			id, _ := r.ObjectID(k)
			if _, e := w.s.Download(ctx, w.p, id); !errors.Is(e, r.ErrNotFound) {
				t.Fatalf("an artifact whose row is swept: %v", e)
			}
		}
		w.store.fail()
		next := w.sweep(r.SweepOptions{Grace: objGrace})
		if next.Artifacts != 0 || next.Orphans != 2 || next.ObjectsLeft != 0 || len(w.keys()) != 0 {
			t.Fatalf("%+v, bucket %v", next, w.keys())
		}
		// A store that cannot even be listed fails the sweep after its rows, with the counts it had.
		w.freeze("again")
		w.store.fail("list")
		if _, e := w.s.Sweep(ctx, r.SweepOptions{Grace: objGrace}); e == nil {
			t.Fatal("a reconciliation that could not list the bucket reported success")
		}
	})
	t.Run("a_sweep_without_the_object_store_deletes_rows_only_and_the_objects_wait_for_a_reconciliation", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		w.export(snap.SnapshotID, "export")
		w.issue(snap.SnapshotID, "issue")
		w.pastGrace()
		dry := w.rowsOnly(r.SweepOptions{Grace: objGrace, DryRun: true})
		if dry.Artifacts != 2 || dry.Objects != 0 || dry.ObjectsLeft != 2 {
			t.Fatalf("%+v", dry)
		}
		res := w.rowsOnly(r.SweepOptions{Grace: objGrace})
		if res.Artifacts != 2 || res.Objects != 0 || res.Orphans != 0 || res.ObjectsLeft != 2 || len(w.keys()) != 2 {
			t.Fatalf("%+v, bucket %v", res, w.keys())
		}
		next := w.sweep(r.SweepOptions{Grace: objGrace})
		if next.Orphans != 2 || len(w.keys()) != 0 {
			t.Fatalf("%+v, bucket %v", next, w.keys())
		}
	})
	t.Run("the_reconciliation_keeps_what_is_young_what_a_row_names_and_what_is_not_ours", func(t *testing.T) {
		w := newWorld(t)
		snap := w.freeze("snap")
		ex := w.export(snap.SnapshotID, "export")
		named := r.ObjectKey(ex.ArtifactID)
		plant := func(key string) {
			t.Helper()
			b := []byte("planted " + key)
			if e := w.raw.Put(ctx, key, b, sha(b)); e != nil {
				t.Fatal(e)
			}
		}
		orphan := r.ObjectKey(r.NewID())
		plant(orphan)
		// Young: nothing is deleted.
		if res := w.sweep(r.SweepOptions{Grace: objGrace}); res.Orphans != 0 || len(w.keys()) != 2 {
			t.Fatalf("%+v", res)
		}
		// Not ours, whatever their age: a key that is not the key of an id.
		strays := []string{r.ObjectPrefix + "readme.txt", r.ObjectPrefix + strings.ToUpper(r.NewID()), r.ObjectPrefix + r.NewID() + ".bak", r.ObjectPrefix + "sub/" + r.NewID()}
		for _, k := range strays {
			plant(k)
		}
		// Old enough to be an orphan (the grace is an hour) and the artifact's row still held (a read lives ten minutes and the sweep keeps a row
		// for the grace after it).
		w.age(objGrace + 5*time.Minute)
		dry := w.sweep(r.SweepOptions{Grace: objGrace, DryRun: true})
		if dry.Orphans != 1 || dry.Artifacts != 0 {
			t.Fatalf("the dry run: %+v", dry)
		}
		res := w.sweep(r.SweepOptions{Grace: objGrace})
		if res.Orphans != 1 || res.Artifacts != 0 || res.Objects != 0 {
			t.Fatalf("%+v", res)
		}
		left := map[string]bool{}
		for _, k := range w.keys() {
			left[k] = true
		}
		if left[orphan] || !left[named] || len(left) != 1+len(strays) {
			t.Fatalf("the bucket holds %v: the orphan must go, the named object and the strays stay", w.keys())
		}
		for _, k := range strays {
			if !left[k] {
				t.Errorf("a key that is not ours was deleted: %q", k)
			}
		}
		// The named object's row is still held (it has expired, and waits for the grace to pass).
		if !w.held("artifact", ex.ArtifactID) {
			t.Fatal("the row of the named object is gone")
		}
		for _, k := range strays {
			_ = w.raw.Delete(ctx, k)
		}
	})
	t.Run("an_object_younger_than_the_floor_is_never_deleted_whatever_grace_is_asked_for", func(t *testing.T) {
		w := newWorld(t)
		orphan := r.ObjectKey(r.NewID())
		b := []byte("an orphan")
		if e := w.raw.Put(ctx, orphan, b, sha(b)); e != nil {
			t.Fatal(e)
		}
		w.age(r.MinOrphanAge - time.Minute)
		if res := w.sweep(r.SweepOptions{Grace: time.Second}); res.Orphans != 0 || len(w.keys()) != 1 {
			t.Fatalf("an object younger than the floor was deleted with a grace of a second: %+v", res)
		}
		w.age(2 * time.Minute)
		if res := w.sweep(r.SweepOptions{Grace: time.Second}); res.Orphans != 1 || len(w.keys()) != 0 {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("an_artifact_stored_before_stays_inline_and_is_served_and_swept_as_before", func(t *testing.T) {
		w := newWorld(t)
		// The legacy service has no object store: its artifact is a row with its content.
		old := w.legacy()
		snap, e := old.SubmitSnapshot(ctx, w.p, contractsim.FinancialQuery(), "snap")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = old.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		sv, _ := old.Job(ctx, w.p, snap.ID)
		legacyExport, e := old.SubmitExport(ctx, w.p, sv.SnapshotID, "legacy")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = old.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		lv, _ := old.Job(ctx, w.p, legacyExport.ID)
		// A new artifact of the same snapshot goes to the object store.
		fresh := w.export(sv.SnapshotID, "fresh")
		row, e := w.reopen().Artifact(ctx, w.p.Identity, lv.ArtifactID)
		if e != nil || row.Object != "" || row.Size != 0 || len(row.Content) == 0 {
			t.Fatalf("the earlier artifact is not inline: %+v %v", row, e)
		}
		if a, e := w.s.Download(ctx, w.p, lv.ArtifactID); e != nil || string(a.Content) != string(row.Content) {
			t.Fatal("an inline artifact is not served as before", e)
		}
		if got := w.keys(); len(got) != 1 || got[0] != r.ObjectKey(fresh.ArtifactID) {
			t.Fatalf("the bucket holds %v", got)
		}
		w.pastGrace()
		res := w.sweep(r.SweepOptions{Grace: objGrace})
		if res.Artifacts != 2 || res.Objects != 1 || res.ObjectsLeft != 0 || res.Orphans != 0 || len(w.keys()) != 0 {
			t.Fatalf("%+v, bucket %v", res, w.keys())
		}
	})
}

// publishWatcher is a repository that looks at a publication before it passes it on, and can refuse it with a failure of its own.
type publishWatcher struct {
	r.Repository
	before func(r.Publication)
	fail   error
}

func (p *publishWatcher) Publish(ctx context.Context, c r.Claim, pub r.Publication) error {
	if p.before != nil {
		p.before(pub)
	}
	if p.fail != nil {
		return p.fail
	}
	return p.Repository.Publish(ctx, c, pub)
}

// lyingStore answers a Get without an error and with bytes the row does not describe, or with an error of its own.
type lyingStore struct {
	r.ObjectStore
	answer []byte
	err    error
}

func (l lyingStore) Get(context.Context, string, string, int) ([]byte, error) {
	return l.answer, l.err
}
