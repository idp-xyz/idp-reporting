package integration_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/contractsim/pgsource"
	"idp.local/reporting/export/xlsx"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
	"os"
	"sync"
	"testing"
	"time"
)

type durableEnv struct {
	s      *r.Service
	repo   *postgres.Store
	source *pgsource.Source
	p      r.Principal
}

func sourceContext(p r.Principal, id string) r.SourceContext {
	return r.SourceContext{Owner: r.Owner{Identity: p.Identity, Integration: id}, SourceTenant: "tenant_a"}
}
func durableService(t *testing.T, repo r.Repository, source r.Adapter, tenants ...string) *r.Service {
	t.Helper()
	connections := []r.Connection{}
	if len(tenants) == 0 {
		tenants = []string{contractsim.DemoPrincipal().Tenant}
	}
	for _, tenant := range tenants {
		for _, id := range []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration} {
			connections = append(connections, r.Connection{Tenant: tenant, Integration: id, SourceTenant: "tenant_a", Adapter: source})
		}
	}
	s, e := r.NewService(r.Config{Repository: repo, Connections: connections, CursorKey: []byte("01234567890123456789012345678901"), Writer: xlsx.Writer{}})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func newDurable(t *testing.T) durableEnv {
	t.Helper()
	ctx := context.Background()
	repo, e := postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(repo.Close)
	source, e := pgsource.Open(ctx, os.Getenv("REPORTING_S01_SOURCE_DSN"), true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(source.Close)
	if e = repo.ResetFixture(ctx); e != nil {
		t.Fatal(e)
	}
	if e = source.ResetFixture(ctx); e != nil {
		t.Fatal(e)
	}
	p := contractsim.DemoPrincipal()
	p.Integrations = append(p.Integrations, contractsim.ShipmentIntegration)
	for i, seed := range []contractsim.Fixture{contractsim.Financial().Fixture(), contractsim.Shipment().Fixture()} {
		id := []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration}[i]
		if e = source.EnsureFixture(ctx, id, "tenant_a", seed); e != nil {
			t.Fatal(e)
		}
		g := contractsim.DefaultGrant()
		if i == 1 {
			g.CurrencyPolicy = nil
		}
		if e = source.EnsureGrant(ctx, sourceContext(p, id), g); e != nil {
			t.Fatal(e)
		}
	}
	return durableEnv{s: durableService(t, repo, source), repo: repo, source: source, p: p}
}
func durableFreeze(t *testing.T, v durableEnv, q r.QuerySpec, key string) r.JobView {
	t.Helper()
	ctx := context.Background()
	j, e := v.s.SubmitSnapshot(ctx, v.p, q, key)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = v.s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	j, e = v.s.Job(ctx, v.p, j.ID)
	if e != nil || j.State != "succeeded" {
		t.Fatal(j, e)
	}
	return j
}
func durableExport(t *testing.T, v durableEnv, id, key string) r.JobView {
	t.Helper()
	ctx := context.Background()
	j, e := v.s.SubmitExport(ctx, v.p, id, key)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = v.s.RunOne(ctx); e != nil {
		t.Fatal(e)
	}
	j, e = v.s.Job(ctx, v.p, j.ID)
	if e != nil || j.State != "succeeded" {
		t.Fatal(j, e)
	}
	return j
}
func fixtureAdmin(t *testing.T) *pgxpool.Pool {
	t.Helper()
	p, e := pgxpool.New(context.Background(), os.Getenv("REPORTING_S01_TEST_DSN"))
	if e != nil {
		t.Fatal("fixture admin could not open")
	}
	t.Cleanup(p.Close)
	return p
}
func inject(t *testing.T, sql string) {
	t.Helper()
	p := fixtureAdmin(t)
	if _, e := p.Exec(context.Background(), sql); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, e := p.Exec(ctx, `DROP TRIGGER IF EXISTS fixture_s01b_fault ON reporting_s01.jobs;DROP TRIGGER IF EXISTS fixture_s01b_fault ON reporting_s01.assets;DROP FUNCTION IF EXISTS reporting_s01.fixture_s01b_fault();`)
		if e != nil {
			t.Error(e)
		}
	})
}

func TestDurablePostgresContract(t *testing.T) {
	if os.Getenv("REPORTING_S01_TEST_DSN") == "" || os.Getenv("REPORTING_S01_SOURCE_DSN") == "" {
		if os.Getenv("REPORTING_S01_REQUIRE_PG") == "1" {
			t.Fatal("required platform/source PostgreSQL DSNs missing")
		}
		t.Skip("durable PostgreSQL gate not run: two isolated DB DSNs required")
	}
	ctx := context.Background()
	t.Run("source_metadata_admission_no_fact_projection", func(t *testing.T) {
		v := newDurable(t)
		j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
		if e != nil || j.State != "queued" || v.source.ReadCalls() != 0 {
			t.Fatal(j, e, v.source.ReadCalls())
		}
		if _, e = v.s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		if v.source.ReadCalls() != 1 {
			t.Fatal(v.source.ReadCalls())
		}
	})
	t.Run("durable_handles_and_old_source_version", func(t *testing.T) {
		v := newDurable(t)
		q := contractsim.FinancialQuery()
		j, e := v.s.SubmitSnapshot(ctx, v.p, q, "one")
		if e != nil {
			t.Fatal(e)
		}
		changed := contractsim.Financial().Fixture()
		changed.Records = []contractsim.Record{{Company: "company_demo", Key: "r1", Values: []string{"changed", "current", "999.00"}}}
		if e = v.source.ReplaceFixture(ctx, q.Integration, "tenant_a", changed); e != nil {
			t.Fatal(e)
		}
		v.source.Close()
		reopened, e := pgsource.Open(ctx, os.Getenv("REPORTING_S01_SOURCE_DSN"), false)
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		v.source = reopened
		v.s = durableService(t, v.repo, reopened)
		if _, e = v.s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		j, e = v.s.Job(ctx, v.p, j.ID)
		if e != nil {
			t.Fatal(e)
		}
		rows, e := v.s.Rows(ctx, v.p, j.SnapshotID, 100, "")
		if e != nil || len(rows.Rows) != 3 || rows.Rows[0][0] != "customer_a" || rows.Totals["ar.open_balance"] != "9007199254740993.01" {
			t.Fatal(rows, e)
		}
		current, e := v.s.Query(ctx, v.p, q)
		if e != nil || current.Totals["ar.open_balance"] != "999.00" {
			t.Fatal(current, e)
		}
	})
	t.Run("seed_never_resurrects_revocation", func(t *testing.T) {
		v := newDurable(t)
		j := durableFreeze(t, v, contractsim.FinancialQuery(), "one")
		sc := sourceContext(v.p, contractsim.FinanceIntegration)
		if e := v.source.Revoke(ctx, sc); e != nil {
			t.Fatal(e)
		}
		v.source.Close()
		source, e := pgsource.Open(ctx, os.Getenv("REPORTING_S01_SOURCE_DSN"), true)
		if e != nil {
			t.Fatal(e)
		}
		defer source.Close()
		if e = source.EnsureFixture(ctx, sc.Owner.Integration, sc.SourceTenant, contractsim.Financial().Fixture()); e != nil {
			t.Fatal(e)
		}
		if e = source.EnsureGrant(ctx, sc, contractsim.DefaultGrant()); e != nil {
			t.Fatal(e)
		}
		s := durableService(t, v.repo, source)
		_, e = s.Rows(ctx, v.p, j.SnapshotID, 1, "")
		mustError(t, e, r.ErrDenied)
		_, e = s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "two")
		mustError(t, e, r.ErrDenied)
	})
	t.Run("owner_matrix_and_idempotency_namespaces", func(t *testing.T) {
		v := newDurable(t)
		s := durableService(t, v.repo, v.source, v.p.Tenant, "other_tenant")
		owners := []r.Principal{}
		base := v.p
		base.Integrations = []string{contractsim.FinanceIntegration}
		owners = append(owners, base)
		other := base
		other.Subject = "user_b"
		owners = append(owners, other)
		other = base
		other.Issuer = "urn:other"
		owners = append(owners, other)
		other = base
		other.Tenant = "other_tenant"
		owners = append(owners, other)
		other = base
		other.Integrations = []string{contractsim.ShipmentIntegration}
		owners = append(owners, other)
		other.Tenant = "other_tenant"
		owners = append(owners, other)
		jobs, files := []r.JobView{}, []r.JobView{}
		seen := map[string]bool{}
		for _, p := range owners {
			id := p.Integrations[0]
			g := contractsim.DefaultGrant()
			q := contractsim.FinancialQuery()
			if id == contractsim.ShipmentIntegration {
				g.CurrencyPolicy = nil
				q = contractsim.ShipmentQuery()
			}
			if e := v.source.EnsureGrant(ctx, sourceContext(p, id), g); e != nil {
				t.Fatal(e)
			}
			instance := v
			instance.p = p
			instance.s = s
			j := durableFreeze(t, instance, q, "same-key")
			ex := durableExport(t, instance, j.SnapshotID, "same-key")
			if seen[j.ID] || seen[j.SnapshotID] || seen[ex.ArtifactID] {
				t.Fatal("namespace collision")
			}
			seen[j.ID] = true
			seen[j.SnapshotID] = true
			seen[ex.ArtifactID] = true
			jobs = append(jobs, j)
			files = append(files, ex)
			replay, e := s.SubmitSnapshot(ctx, p, q, "same-key")
			if e != nil || replay.ID != j.ID {
				t.Fatal(replay, e)
			}
		}
		for i, p := range owners {
			for k, j := range jobs {
				if i == k {
					continue
				}
				want := r.ErrNotFound
				if p.Identity == owners[k].Identity {
					want = r.ErrDenied
				}
				_, e := s.Job(ctx, p, j.ID)
				mustError(t, e, want)
				_, e = s.Rows(ctx, p, j.SnapshotID, 1, "")
				mustError(t, e, want)
				_, e = s.Download(ctx, p, files[k].ArtifactID)
				mustError(t, e, want)
				_, e = s.Cancel(ctx, p, j.ID)
				mustError(t, e, want)
			}
		}
	})
	t.Run("durable_source_scope_and_policy_binding", func(t *testing.T) {
		v := newDurable(t)
		j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
		if e != nil {
			t.Fatal(e)
		}
		g := contractsim.DefaultGrant()
		g.Rows = append(g.Rows, "secret")
		g.Companies = append(g.Companies, "company_other")
		sc := sourceContext(v.p, contractsim.FinanceIntegration)
		if e = v.source.SetGrant(ctx, sc, g); e != nil {
			t.Fatal(e)
		}
		if _, e = v.s.RunOne(ctx); e != nil {
			t.Fatal(e)
		}
		j, e = v.s.Job(ctx, v.p, j.ID)
		if e != nil {
			t.Fatal(e)
		}
		rows, e := v.s.Rows(ctx, v.p, j.SnapshotID, 100, "")
		if e != nil || len(rows.Rows) != 3 || rows.Totals["ar.open_balance"] != "9007199254740993.01" {
			t.Fatal(rows, e)
		}
		g.Timezone = "UTC"
		if e = v.source.SetGrant(ctx, sc, g); e != nil {
			t.Fatal(e)
		}
		_, e = v.s.Rows(ctx, v.p, j.SnapshotID, 1, "")
		mustError(t, e, r.ErrDenied)
	})
	t.Run("source_handle_cannot_cross_owner_or_product", func(t *testing.T) {
		v := newDurable(t)
		sc := sourceContext(v.p, contractsim.FinanceIntegration)
		q := contractsim.FinancialQuery()
		p, e := v.source.PrepareRead(ctx, sc, q, r.QueryHash(q))
		if e != nil {
			t.Fatal(e)
		}
		bad := sc
		bad.Owner.Integration = contractsim.ShipmentIntegration
		mustError(t, v.source.AuthorizeCurrent(ctx, bad, p), r.ErrDenied)
		bad = sc
		bad.Owner.Subject = "user_b"
		mustError(t, v.source.AuthorizeCurrent(ctx, bad, p), r.ErrDenied)
		bad = sc
		bad.SourceTenant = "other_source"
		mustError(t, v.source.AuthorizeCurrent(ctx, bad, p), r.ErrDenied)
	})
	t.Run("durable_outage_blocks_cached_file", func(t *testing.T) {
		v := newDurable(t)
		j := durableFreeze(t, v, contractsim.FinancialQuery(), "one")
		ex := durableExport(t, v, j.SnapshotID, "one")
		if e := v.source.SetUnavailable(ctx, contractsim.FinanceIntegration, "tenant_a", true); e != nil {
			t.Fatal(e)
		}
		_, e := v.s.Download(ctx, v.p, ex.ArtifactID)
		mustError(t, e, r.ErrUnavailable)
		v.source.Close()
		_, e = v.s.Rows(ctx, v.p, j.SnapshotID, 1, "")
		mustError(t, e, r.ErrUnavailable)
	})
	t.Run("duplicate_publication_has_one_asset", func(t *testing.T) {
		v := newDurable(t)
		_, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
		if e != nil {
			t.Fatal(e)
		}
		c, e := v.repo.Claim(ctx, time.Minute)
		if e != nil || c == nil {
			t.Fatal(c, e)
		}
		pubs := []r.Publication{publication(*c), publication(*c)}
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		for _, p := range pubs {
			wg.Add(1)
			go func(p r.Publication) { defer wg.Done(); errs <- v.repo.Publish(ctx, *c, p) }(p)
		}
		wg.Wait()
		close(errs)
		success, losers := 0, 0
		for e := range errs {
			if e == nil {
				success++
			} else if errors.Is(e, r.ErrLease) {
				losers++
			} else {
				t.Fatal(e)
			}
		}
		if success != 1 || losers != 1 {
			t.Fatal(success, losers)
		}
		assets := 0
		for _, p := range pubs {
			_, e := v.repo.Snapshot(ctx, v.p.Identity, p.Snapshot.ID)
			if e == nil {
				assets++
			} else {
				mustError(t, e, r.ErrNotFound)
			}
		}
		if assets != 1 {
			t.Fatal(assets)
		}
	})
	t.Run("cancel_publish_race_is_atomic", func(t *testing.T) {
		v := newDurable(t)
		for i := 0; i < 8; i++ {
			j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), fmt.Sprint(i))
			if e != nil {
				t.Fatal(e)
			}
			c, e := v.repo.Claim(ctx, time.Minute)
			if e != nil || c == nil {
				t.Fatal(c, e)
			}
			pub := publication(*c)
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			var pe, ce error
			go func() { defer wg.Done(); <-start; pe = v.repo.Publish(ctx, *c, pub) }()
			go func() { defer wg.Done(); <-start; _, ce = v.repo.Cancel(ctx, v.p.Identity, j.ID) }()
			close(start)
			wg.Wait()
			if ce != nil {
				t.Fatal(ce)
			}
			state, e := v.repo.Job(ctx, v.p.Identity, j.ID)
			if e != nil {
				t.Fatal(e)
			}
			_, ae := v.repo.Snapshot(ctx, v.p.Identity, pub.Snapshot.ID)
			if state.State == "cancelled" {
				mustError(t, pe, r.ErrLease)
				mustError(t, ae, r.ErrNotFound)
			} else if state.State == "succeeded" {
				if pe != nil || ae != nil {
					t.Fatal(pe, ae)
				}
			} else {
				t.Fatal(state)
			}
		}
	})
	t.Run("rollback_after_asset_insert", func(t *testing.T) {
		v := newDurable(t)
		j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
		if e != nil {
			t.Fatal(e)
		}
		c, e := v.repo.Claim(ctx, time.Minute)
		if e != nil || c == nil {
			t.Fatal(c, e)
		}
		pub := publication(*c)
		inject(t, `CREATE FUNCTION reporting_s01.fixture_s01b_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='succeeded' THEN RAISE EXCEPTION 'fixture injected final update failure'; END IF; RETURN NEW; END $$;CREATE TRIGGER fixture_s01b_fault BEFORE UPDATE ON reporting_s01.jobs FOR EACH ROW EXECUTE FUNCTION reporting_s01.fixture_s01b_fault();`)
		if e = v.repo.Publish(ctx, *c, pub); e == nil {
			t.Fatal("fault did not abort publication")
		}
		_, e = v.repo.Snapshot(ctx, v.p.Identity, pub.Snapshot.ID)
		mustError(t, e, r.ErrNotFound)
		state, e := v.repo.Job(ctx, v.p.Identity, j.ID)
		if e != nil || state.State != "running" || state.SnapshotID != "" {
			t.Fatal(state, e)
		}
	})
	t.Run("lease_expires_inside_publication_transaction", func(t *testing.T) {
		v := newDurable(t)
		_, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
		if e != nil {
			t.Fatal(e)
		}
		c, e := v.repo.Claim(ctx, time.Second)
		if e != nil || c == nil {
			t.Fatal(c, e)
		}
		pub := publication(*c)
		inject(t, `CREATE FUNCTION reporting_s01.fixture_s01b_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(1.1); RETURN NEW; END $$;CREATE TRIGGER fixture_s01b_fault BEFORE INSERT ON reporting_s01.assets FOR EACH ROW EXECUTE FUNCTION reporting_s01.fixture_s01b_fault();`)
		mustError(t, v.repo.Publish(ctx, *c, pub), r.ErrLease)
		_, e = v.repo.Snapshot(ctx, v.p.Identity, pub.Snapshot.ID)
		mustError(t, e, r.ErrNotFound)
	})
	t.Run("corrupt_asset_payload_fails_before_disclosure", func(t *testing.T) {
		v := newDurable(t)
		j := durableFreeze(t, v, contractsim.FinancialQuery(), "one")
		ex := durableExport(t, v, j.SnapshotID, "one")
		p := fixtureAdmin(t)
		_, e := p.Exec(ctx, `UPDATE reporting_s01.assets SET data=jsonb_set(data,'{Content}','"Y29ycnVwdA=="') WHERE id=$1`, ex.ArtifactID)
		if e != nil {
			t.Fatal(e)
		}
		_, e = v.s.Download(ctx, v.p, ex.ArtifactID)
		mustError(t, e, r.ErrIntegrity)
	})
	t.Run("sql_payload_owner_mismatch_fails_even_with_hash", func(t *testing.T) {
		v := newDurable(t)
		j := durableFreeze(t, v, contractsim.FinancialQuery(), "one")
		p := fixtureAdmin(t)
		_, e := p.Exec(ctx, `UPDATE reporting_s01.assets SET data=jsonb_set(data,'{Owner,Subject}','"other_user"') WHERE id=$1`, j.SnapshotID)
		if e != nil {
			t.Fatal(e)
		}
		_, e = p.Exec(ctx, `UPDATE reporting_s01.assets SET data_sha256=encode(sha256(convert_to(data::text,'UTF8')),'hex') WHERE id=$1`, j.SnapshotID)
		if e != nil {
			t.Fatal(e)
		}
		_, e = v.s.Rows(ctx, v.p, j.SnapshotID, 1, "")
		mustError(t, e, r.ErrIntegrity)
	})
	t.Run("job_payload_hash_rejects_corruption", func(t *testing.T) {
		v := newDurable(t)
		j, e := v.s.SubmitSnapshot(ctx, v.p, contractsim.FinancialQuery(), "one")
		if e != nil {
			t.Fatal(e)
		}
		p := fixtureAdmin(t)
		if _, e = p.Exec(ctx, `UPDATE reporting_s01.jobs SET data=jsonb_set(data,'{Query,as_of}','"2026-10-25"') WHERE id=$1`, j.ID); e != nil {
			t.Fatal(e)
		}
		_, e = v.s.Job(ctx, v.p, j.ID)
		mustError(t, e, r.ErrIntegrity)
	})
	t.Run("unreadable_job_is_failed_at_its_claim_and_never_claimed_again", durableUnreadableJobs)
	t.Run("fixture_v1_upgrade_preserves_existing_assets", func(t *testing.T) {
		v := newDurable(t)
		j := durableFreeze(t, v, contractsim.FinancialQuery(), "one")
		ex := durableExport(t, v, j.SnapshotID, "one")
		before, e := v.s.Download(ctx, v.p, ex.ArtifactID)
		if e != nil {
			t.Fatal(e)
		}
		v.repo.Close()
		p := fixtureAdmin(t)
		// A database made before the ledger existed (S04-A): the profile's version alone says
		// what was applied; the migrator bootstraps the ledger from it and upgrades.
		if _, e = p.Exec(ctx, `ALTER TABLE reporting_s01.jobs DROP COLUMN data_sha256;ALTER TABLE reporting_s01.assets DROP COLUMN data_sha256;DROP TABLE reporting_s01.schema_migrations;ALTER TABLE reporting_s01.jobs DROP CONSTRAINT jobs_kind_check;ALTER TABLE reporting_s01.jobs ADD CONSTRAINT jobs_kind_check CHECK(kind IN ('snapshot','export'));ALTER TABLE reporting_s01.assets DROP CONSTRAINT assets_kind_check;ALTER TABLE reporting_s01.assets ADD CONSTRAINT assets_kind_check CHECK(kind IN ('snapshot','artifact'));UPDATE reporting_s01.profile SET version=1 WHERE singleton;`); e != nil {
			t.Fatal(e)
		}
		bad, e := postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), false)
		if bad != nil {
			bad.Close()
		}
		mustError(t, e, r.ErrDenied)
		reopened, e := postgres.Open(ctx, os.Getenv("REPORTING_S01_TEST_DSN"), true)
		if e != nil {
			t.Fatal(e)
		}
		defer reopened.Close()
		s := durableService(t, reopened, v.source)
		after, e := s.Download(ctx, v.p, ex.ArtifactID)
		if e != nil || after.SHA256 != before.SHA256 {
			t.Fatal(e)
		}
		state, e := s.Job(ctx, v.p, j.ID)
		if e != nil || state.SnapshotID != j.SnapshotID {
			t.Fatal(state, e)
		}
	})
	t.Run("company_selection_is_never_authority", func(t *testing.T) {
		v := newDurable(t)
		q := contractsim.FinancialQuery()
		q.Companies = []string{"company_other"}
		_, e := v.s.SubmitSnapshot(ctx, v.p, q, "one")
		mustError(t, e, r.ErrDenied)
		if v.source.ReadCalls() != 0 {
			t.Fatal("unauthorized request projected facts")
		}
	})
	t.Run("currency_change_survives_source_reopen", func(t *testing.T) {
		v := newDurable(t)
		j := durableFreeze(t, v, contractsim.FinancialQuery(), "one")
		ex := durableExport(t, v, j.SnapshotID, "one")
		g := contractsim.DefaultGrant()
		g.CurrencyPolicy = &r.Ref{ID: "ledger_base", Version: "2.0.0"}
		sc := sourceContext(v.p, contractsim.FinanceIntegration)
		if e := v.source.SetGrant(ctx, sc, g); e != nil {
			t.Fatal(e)
		}
		v.source.Close()
		source, e := pgsource.Open(ctx, os.Getenv("REPORTING_S01_SOURCE_DSN"), true)
		if e != nil {
			t.Fatal(e)
		}
		defer source.Close()
		if e = source.EnsureGrant(ctx, sc, contractsim.DefaultGrant()); e != nil {
			t.Fatal(e)
		}
		s := durableService(t, v.repo, source)
		_, e = s.Rows(ctx, v.p, j.SnapshotID, 1, "")
		mustError(t, e, r.ErrDenied)
		_, e = s.Download(ctx, v.p, ex.ArtifactID)
		mustError(t, e, r.ErrDenied)
		_, e = s.SubmitExport(ctx, v.p, j.SnapshotID, "two")
		mustError(t, e, r.ErrDenied)
	})
	t.Run("corrupt_source_handle_denies_cached_artifact", func(t *testing.T) {
		v := newDurable(t)
		j := durableFreeze(t, v, contractsim.FinancialQuery(), "one")
		ex := durableExport(t, v, j.SnapshotID, "one")
		admin, e := pgxpool.New(ctx, os.Getenv("REPORTING_S01_SOURCE_DSN"))
		if e != nil {
			t.Fatal("source fixture admin unavailable")
		}
		defer admin.Close()
		if _, e = admin.Exec(ctx, `UPDATE reporting_source_s01.prepared SET data=jsonb_set(data,'{Prepared,ScopeHandle}','"corrupt"')`); e != nil {
			t.Fatal(e)
		}
		_, e = v.s.Download(ctx, v.p, ex.ArtifactID)
		mustError(t, e, r.ErrIntegrity)
	})
	t.Run("multiple_source_pages_stay_on_original_durable_version", func(t *testing.T) {
		v := newDurable(t)
		data, g := records(60)
		seed := contractsim.Financial().Fixture()
		seed.Records = data
		if e := v.source.ReplaceFixture(ctx, contractsim.FinanceIntegration, "tenant_a", seed); e != nil {
			t.Fatal(e)
		}
		if e := v.source.SetGrant(ctx, sourceContext(v.p, contractsim.FinanceIntegration), g); e != nil {
			t.Fatal(e)
		}
		var once sync.Once
		wrapped := intercept{Adapter: v.source, afterRead: func() {
			once.Do(func() {
				changed := contractsim.Financial().Fixture()
				changed.Records = []contractsim.Record{{Company: "company_demo", Key: "row_0000", Values: []string{"changed", "current", "999.00"}}}
				if e := v.source.ReplaceFixture(ctx, contractsim.FinanceIntegration, "tenant_a", changed); e != nil {
					t.Fatal(e)
				}
			})
		}}
		s := durableService(t, v.repo, wrapped)
		q := contractsim.FinancialQuery()
		q.Page.Limit = 100
		result, e := s.Query(ctx, v.p, q)
		if e != nil || len(result.Rows) != 60 || result.Rows[59][0] != "row_0059" || result.Totals["ar.open_balance"] != "60.00" || v.source.ReadCalls() != 2 {
			t.Fatal(len(result.Rows), result.Totals, v.source.ReadCalls(), e)
		}
	})
}
