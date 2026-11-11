package reporting_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"idp.local/reporting/adapters/contractsim"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/memory"
)

// classed is the fake adapter of a data class (S08-A): the receivables fixture served by an adapter that says the word it is given.
type classed struct {
	*contractsim.Simulator
	class string
}

func (c classed) DataClass() string { return c.class }

// silent is an adapter that says no data class, as the adapter of a source product needs none: it forwards the four source calls.
type silent struct{ r.Adapter }

// classWriter is an export writer that records the data class of every snapshot it is given.
type classWriter struct{ seen *[]string }

func (w classWriter) Write(_ context.Context, s r.Snapshot) ([]byte, error) {
	*w.seen = append(*w.seen, s.Result.Evidence.DataClass)
	return []byte("file"), nil
}

// classWorld is a service over one connection per integration id, each a receivables fixture of its own behind the adapter the test
// wraps it in, and a principal that holds them all.
func classWorld(t *testing.T, repo r.Repository, wrap map[string]func(*contractsim.Simulator) r.Adapter, sims map[string]*contractsim.Simulator, w r.WorkbookWriter) (*r.Service, r.Principal, error) {
	t.Helper()
	p := contractsim.DemoPrincipal()
	p.Integrations = nil
	var connections []r.Connection
	for _, id := range slices.Sorted(maps.Keys(wrap)) {
		p.Integrations = append(p.Integrations, id)
		if sims[id] == nil {
			sims[id] = contractsim.Financial()
			sims[id].Grant(contractsim.DemoOwner(p, id), contractsim.DefaultGrant())
		}
		connections = append(connections, r.Connection{Tenant: p.Tenant, Integration: id, SourceTenant: "tenant_a", Adapter: wrap[id](sims[id])})
	}
	s, e := r.NewService(r.Config{Repository: repo, Connections: connections, CursorKey: []byte("01234567890123456789012345678901"), Writer: w})
	return s, p, e
}

func queryOfIntegration(id string) r.QuerySpec {
	q := contractsim.FinancialQuery()
	q.Integration = id
	return q
}

// freezeAs freezes the integration's dataset through the service and returns the snapshot's id.
func freezeAs(t *testing.T, s *r.Service, p r.Principal, id, key string) string {
	t.Helper()
	ctx := context.Background()
	j, e := s.SubmitSnapshot(ctx, p, queryOfIntegration(id), key)
	if e != nil {
		t.Fatal(id, e)
	}
	if _, e = s.RunOne(ctx); e != nil {
		t.Fatal(id, e)
	}
	if j, e = s.Job(ctx, p, j.ID); e != nil || j.State != "succeeded" || j.SnapshotID == "" {
		t.Fatal(id, j, e)
	}
	return j.SnapshotID
}

// TestTheDataClassIsTheAdaptersWord (S08-A, ADR 0012 decision 6): the evidence of a result says where its data comes from in the word of
// the adapter it was read through, one of three: a contract simulator's, a manual source's, a source product's; an adapter that says
// nothing serves a source product's data. The same word is in a preview's evidence, in the frozen snapshot's, on every page of it, and
// in what the export worker hands a writer; the service says it for a product the principal holds, and for no other.
func TestTheDataClassIsTheAdaptersWord(t *testing.T) {
	if !slices.Equal(r.DataClasses, []string{"contract-simulator", "manual-entry", "business-data"}) || r.DataClassSimulator != "contract-simulator" || r.DataClassManual != "manual-entry" || r.DataClassBusiness != "business-data" {
		t.Fatalf("the closed set is %q", r.DataClasses)
	}
	for word, valid := range map[string]bool{"contract-simulator": true, "manual-entry": true, "business-data": true, "": false, "simulator": false, "Business-Data": false, "business-data ": false, "real": false} {
		if r.ValidDataClass(word) != valid {
			t.Errorf("ValidDataClass(%q) = %v", word, !valid)
		}
	}
	want := map[string]string{"sim": "contract-simulator", "manual": "manual-entry", "product": "business-data", "silent": "business-data"}
	wrap := map[string]func(*contractsim.Simulator) r.Adapter{
		"sim":     func(s *contractsim.Simulator) r.Adapter { return s }, // the simulator's own word
		"manual":  func(s *contractsim.Simulator) r.Adapter { return classed{s, "manual-entry"} },
		"product": func(s *contractsim.Simulator) r.Adapter { return classed{s, "business-data"} },
		"silent":  func(s *contractsim.Simulator) r.Adapter { return silent{s} },
	}
	// What an adapter says, before any service: its own word, or business-data when it says none.
	for id, w := range wrap {
		if got := r.DataClassOf(w(contractsim.Financial())); got != want[id] {
			t.Errorf("DataClassOf(%s) = %q, want %q", id, got, want[id])
		}
	}
	var written []string
	repo := memory.New(time.Now)
	s, p, e := classWorld(t, repo, wrap, map[string]*contractsim.Simulator{}, classWriter{&written})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	for _, id := range []string{"sim", "manual", "product", "silent"} {
		if got, e := s.DataClass(p, id); e != nil || got != want[id] {
			t.Errorf("%s: the service says %q (%v), want %q", id, got, e, want[id])
		}
		res, e := s.Query(ctx, p, queryOfIntegration(id))
		if e != nil || len(res.Rows) != 3 || res.Evidence.DataClass != want[id] {
			t.Errorf("%s: a query's evidence says %q (%v), want %q", id, res.Evidence.DataClass, e, want[id])
		}
		snapshot := freezeAs(t, s, p, id, "snap-"+id)
		stored, e := repo.Snapshot(ctx, p.Identity, snapshot)
		if e != nil || stored.Result.Evidence.DataClass != want[id] {
			t.Errorf("%s: the stored snapshot says %q (%v), want %q", id, stored.Result.Evidence.DataClass, e, want[id])
		}
		page, e := s.Rows(ctx, p, snapshot, 2, "")
		if e != nil || page.Evidence.DataClass != want[id] || page.NextCursor == "" {
			t.Errorf("%s: a page of the snapshot says %q (%v), want %q", id, page.Evidence.DataClass, e, want[id])
		}
		if page, e = s.Rows(ctx, p, snapshot, 2, page.NextCursor); e != nil || page.Evidence.DataClass != want[id] {
			t.Errorf("%s: the next page says %q (%v), want %q", id, page.Evidence.DataClass, e, want[id])
		}
		written = nil
		if _, e = s.SubmitExport(ctx, p, snapshot, "export-"+id); e != nil {
			t.Fatal(id, e)
		}
		if _, e = s.RunOne(ctx); e != nil || !slices.Equal(written, []string{want[id]}) {
			t.Errorf("%s: the writer was given %q (%v), want %q", id, written, e, want[id])
		}
	}
	// The word is said for a product the principal holds: another product is refused as its catalogue is, and so is no identity.
	if _, e := s.DataClass(p, "elsewhere"); !errors.Is(e, r.ErrDenied) {
		t.Errorf("a product the principal does not hold: %v", e)
	}
	stranger := p
	stranger.Integrations = []string{"manual"}
	if _, e := s.DataClass(stranger, "sim"); !errors.Is(e, r.ErrDenied) {
		t.Errorf("a product outside the principal's integrations: %v", e)
	}
	if _, e := s.DataClass(r.Principal{}, "sim"); !errors.Is(e, r.ErrDenied) {
		t.Errorf("no identity: %v", e)
	}
}

// TestNewServiceRefusesAnAdapterThatSaysAnotherWord: the set is closed. A connection whose adapter says a word outside it (or says one
// and it is empty) is refused when the service is made, so no result is ever labelled with a word the exports do not know.
func TestNewServiceRefusesAnAdapterThatSaysAnotherWord(t *testing.T) {
	for _, word := range []string{"", "real-data", "simulator", "Contract-Simulator", "business-data ", " manual-entry", "contract-simulator,business-data"} {
		wrap := map[string]func(*contractsim.Simulator) r.Adapter{
			"good": func(s *contractsim.Simulator) r.Adapter { return s },
			"odd":  func(s *contractsim.Simulator) r.Adapter { return classed{s, word} },
		}
		if s, _, e := classWorld(t, memory.New(time.Now), wrap, map[string]*contractsim.Simulator{}, nil); !errors.Is(e, r.ErrInvalid) || s != nil {
			t.Errorf("an adapter that says %q: %v, want the service refused", word, e)
		}
	}
	for _, word := range r.DataClasses {
		wrap := map[string]func(*contractsim.Simulator) r.Adapter{"one": func(s *contractsim.Simulator) r.Adapter { return classed{s, word} }}
		if s, _, e := classWorld(t, memory.New(time.Now), wrap, map[string]*contractsim.Simulator{}, nil); e != nil || s == nil {
			t.Errorf("an adapter that says %q: %v", word, e)
		}
	}
}

// TestAStoredSnapshotKeepsTheWordItWasFrozenWith: the class is read from the adapter when a result is collected and stays with the
// frozen snapshot. A server restarted with an adapter that says another word serves and exports the old snapshot under the word it
// was frozen with, and labels what it reads from then on with the new one.
func TestAStoredSnapshotKeepsTheWordItWasFrozenWith(t *testing.T) {
	ctx := context.Background()
	repo := memory.New(time.Now)
	sims := map[string]*contractsim.Simulator{}
	var written []string
	before, p, e := classWorld(t, repo, map[string]func(*contractsim.Simulator) r.Adapter{"ledger": func(s *contractsim.Simulator) r.Adapter { return classed{s, "manual-entry"} }}, sims, classWriter{&written})
	if e != nil {
		t.Fatal(e)
	}
	snapshot := freezeAs(t, before, p, "ledger", "snap")
	// The same store and the same source behind an adapter that now says it serves a source product's data.
	after, _, e := classWorld(t, repo, map[string]func(*contractsim.Simulator) r.Adapter{"ledger": func(s *contractsim.Simulator) r.Adapter { return classed{s, "business-data"} }}, sims, classWriter{&written})
	if e != nil {
		t.Fatal(e)
	}
	if got, _ := after.DataClass(p, "ledger"); got != "business-data" {
		t.Fatalf("the connection now says %q", got)
	}
	page, e := after.Rows(ctx, p, snapshot, 10, "")
	if e != nil || page.Evidence.DataClass != "manual-entry" || len(page.Rows) != 3 {
		t.Fatalf("the stored snapshot is served as %q (%v), want the word it was frozen with", page.Evidence.DataClass, e)
	}
	if _, e = after.SubmitExport(ctx, p, snapshot, "export"); e != nil {
		t.Fatal(e)
	}
	if _, e = after.RunOne(ctx); e != nil || !slices.Equal(written, []string{"manual-entry"}) {
		t.Fatalf("the stored snapshot is exported as %q (%v)", written, e)
	}
	res, e := after.Query(ctx, p, queryOfIntegration("ledger"))
	if e != nil || res.Evidence.DataClass != "business-data" {
		t.Fatalf("a new read says %q (%v)", res.Evidence.DataClass, e)
	}
	if again := freezeAs(t, after, p, "ledger", "snap-after"); again == snapshot {
		t.Fatal("the new snapshot is the old one")
	} else if stored, e := repo.Snapshot(ctx, p.Identity, again); e != nil || stored.Result.Evidence.DataClass != "business-data" {
		t.Fatalf("a new snapshot says %q (%v)", stored.Result.Evidence.DataClass, e)
	}
}

// rewording is a store that answers every snapshot with another data class in its evidence, the digest made to fit: what a store that
// held a word the platform does not know would answer.
type rewording struct {
	r.Repository
	class string
}

func (w rewording) Snapshot(ctx context.Context, i r.Identity, id string) (r.Snapshot, error) {
	v, e := w.Repository.Snapshot(ctx, i, id)
	if e == nil {
		v.Result.Evidence.DataClass = w.class
		v.SHA256 = r.Digest(v.Result)
	}
	return v, e
}

// TestStoredEvidenceIsHeldToTheClosedSet: a snapshot is never published with a data class outside the set, or with none, and one that a
// store answers with such a word is an integrity failure: no page of it is served, it is not exported and not handed to a renderer.
func TestStoredEvidenceIsHeldToTheClosedSet(t *testing.T) {
	ctx := context.Background()
	repo := memory.New(time.Now)
	sims := map[string]*contractsim.Simulator{}
	wrap := map[string]func(*contractsim.Simulator) r.Adapter{"ledger": func(s *contractsim.Simulator) r.Adapter { return s }}
	s, p, e := classWorld(t, repo, wrap, sims, classWriter{new([]string)})
	if e != nil {
		t.Fatal(e)
	}
	j, e := s.SubmitSnapshot(ctx, p, queryOfIntegration("ledger"), "snap")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	job, e := repo.Job(ctx, p.Identity, j.ID)
	if e != nil || job.SnapshotID == "" {
		t.Fatal(job, e)
	}
	stored, e := repo.Snapshot(ctx, p.Identity, job.SnapshotID)
	if e != nil {
		t.Fatal(e)
	}
	// The publication the stores hold a snapshot job to (both call ValidatePublication): each of the three words, and nothing else.
	publish := func(class string) error {
		v := stored
		v.Result.Evidence.DataClass = class
		v.SHA256 = r.Digest(v.Result)
		return r.ValidatePublication(job, r.Publication{Snapshot: &v})
	}
	for _, class := range []string{"contract-simulator", "manual-entry", "business-data"} {
		if e := publish(class); e != nil {
			t.Errorf("a snapshot whose evidence says %q is not published: %v", class, e)
		}
	}
	for _, class := range []string{"", "real-data", "CONTRACT-SIMULATOR", "contract-simulator ", "合同模拟器"} {
		if e := publish(class); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("a snapshot whose evidence says %q: %v, want an integrity failure", class, e)
		}
	}
	// A store that answers a snapshot with a word outside the set: nothing of it is disclosed or rendered.
	for _, class := range []string{"", "real-data"} {
		odd, _, e := classWorld(t, rewording{repo, class}, wrap, sims, classWriter{new([]string)})
		if e != nil {
			t.Fatal(e)
		}
		if _, e := odd.Rows(ctx, p, job.SnapshotID, 10, ""); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("a page of a snapshot stored as %q: %v", class, e)
		}
		if _, e := odd.SubmitExport(ctx, p, job.SnapshotID, "export-"+class); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("an export of a snapshot stored as %q: %v", class, e)
		}
		if _, e := odd.AuthorizedSnapshot(ctx, p, job.SnapshotID); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("a snapshot stored as %q for a renderer: %v", class, e)
		}
		if view, e := odd.Job(ctx, p, j.ID); !errors.Is(e, r.ErrIntegrity) {
			t.Errorf("the job of a snapshot stored as %q: %+v %v", class, view, e)
		}
	}
	// A store that answers it with another word of the set serves it under that word: the stored word is the snapshot's.
	manual, _, e := classWorld(t, rewording{repo, "manual-entry"}, wrap, sims, classWriter{new([]string)})
	if e != nil {
		t.Fatal(e)
	}
	if page, e := manual.Rows(ctx, p, job.SnapshotID, 10, ""); e != nil || page.Evidence.DataClass != "manual-entry" {
		t.Errorf("a snapshot stored as manual-entry: %q (%v)", page.Evidence.DataClass, e)
	}
}
