package integration_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"idp.local/reporting/internal/pgauthproxy"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/postgres"
	"idp.local/reporting/transport/httpapi"
)

// credentialRotation proves the rotation of the database passwords of S04-H (docs/S04H_SCOPE.md H3, docs/OPERATIONS.md "Keys and
// credentials") on the roles of the migration qualification: the application role through the production opener and the HTTP API, the sweep
// role through the sweep. It runs inside that qualification, with the production profile row, the application role and the sweep role in place.
//
// The throwaway PostgreSQL server authenticates by trust, which accepts any password, so the connections go through internal/pgauthproxy: it
// runs SCRAM-SHA-256 against the verifier the server stores for the role (read at every connection) and relays to the server only a
// connection that passed. ALTER ROLE ... PASSWORD, the sessions and the verifier are the server's own. That a PostgreSQL configured for
// scram-sha-256 does the same is PostgreSQL's documented behaviour; this test shows it for the verifier the server really stored, and not for
// a server that enforces it.
func credentialRotation(t *testing.T, admin *pgxpool.Pool, env durableEnv, jobID string) {
	t.Helper()
	ctx := context.Background()
	upstream, err := url.Parse(os.Getenv("REPORTING_S01_TEST_DSN"))
	if err != nil {
		t.Fatal("the fixture DSN does not parse")
	}
	proxy := pgauthproxy.Start(t, upstream.Host, pgauthproxy.Admin{Pool: admin})
	database := currentDatabase(t, admin)
	dsn := func(role, password string) string {
		u := url.URL{Scheme: "postgresql", User: url.UserPassword(role, password), Host: proxy.Addr, Path: "/" + database, RawQuery: "sslmode=disable&application_name=s04h-rotation"}
		return u.String()
	}
	setPassword := func(role, password string) {
		t.Helper()
		if _, err := admin.Exec(ctx, "ALTER ROLE "+role+" PASSWORD '"+password+"'"); err != nil {
			t.Fatal(err)
		}
	}
	verifier := func(role string) string {
		t.Helper()
		v, err := pgauthproxy.Admin{Pool: admin}.Verifier(ctx, role)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	const appOld, appNew = "app-OLD-password-5e1f90a7c3", "app-NEW-password-b84d2e61f0"
	const sweepOld, sweepNew = "sweep-OLD-password-91c0a4d7e2", "sweep-NEW-password-37be5f08a1"
	secrets := []string{appOld, appNew, sweepOld, sweepNew}
	noSecret := func(what, text string) {
		t.Helper()
		for _, s := range secrets {
			if strings.Contains(text, s) {
				t.Errorf("%s carries a password: %q", what, text)
			}
		}
		for _, leak := range []string{"SCRAM-SHA-256$", "postgresql://", "password="} {
			if strings.Contains(text, leak) {
				t.Errorf("%s carries %q: %q", what, leak, text)
			}
		}
	}
	// A pool that is all one connection, so that a second acquire has to dial.
	pool := func(role, password string) *pgxpool.Pool {
		t.Helper()
		c, err := pgxpool.ParseConfig(dsn(role, password))
		if err != nil {
			t.Fatal(err)
		}
		c.MaxConns, c.MinConns = 4, 0
		c.ConnConfig.ConnectTimeout = 3 * time.Second
		p, err := pgxpool.NewWithConfig(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}

	// The control: the server's verifier is a SCRAM one that holds no password, and the front refuses a password that was never set.
	setPassword("reporting_app_t", appOld)
	if v := verifier("reporting_app_t"); !strings.HasPrefix(v, "SCRAM-SHA-256$") || strings.Contains(v, appOld) {
		t.Fatal("the stored password is not a salted verifier")
	}
	before := proxy.Refused.Load()
	if _, err := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: dsn("reporting_app_t", "never-the-password-0000"), InsecureTestOnly: true}); err == nil {
		t.Fatal("a wrong password opened the production store")
	} else {
		noSecret("a refusal of a wrong password", err.Error())
	}
	if proxy.Refused.Load() != before+1 {
		t.Fatal("the password check did not run")
	}

	// The running server: the production store with the old password, serving the API, and a pool with a connection held.
	server, err := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: dsn("reporting_app_t", appOld), InsecureTestOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	service := issueDurableService(t, durableEnv{repo: server, source: env.source, p: env.p}, &stampIssuer{})
	const bearer = "credential-rotation-bearer-0123456789abcdef"
	auth, err := httpapi.DemoBearer(bearer, env.p)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.Handler(service, auth)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) (int, string) {
		req := httptest.NewRequest("GET", "/api/reporting/v1/"+path, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if code, body := get("jobs/" + jobID); code != 200 {
		t.Fatal("the running server does not serve before the rotation", code, body)
	}
	oldPool := pool("reporting_app_t", appOld)
	held, err := oldPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()

	// The change, on the server. Nothing else is restarted.
	setPassword("reporting_app_t", appNew)
	if verifier("reporting_app_t") == "" {
		t.Fatal("no verifier")
	}

	// 1. Connections already open keep working: the held connection, and the running server's pooled one.
	var one int
	if err := held.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatal("a connection that was open stopped working at the change", err)
	}
	if code, body := get("jobs/" + jobID); code != 200 {
		t.Fatal("the running server stopped serving at the change", code, body)
	}
	// 2. A new connection with the old password fails, with a text that carries no password.
	before = proxy.Refused.Load()
	second, err := oldPool.Acquire(ctx)
	if err == nil {
		second.Release()
		t.Fatal("a new connection with the old password was accepted")
	}
	noSecret("a new connection's refusal", err.Error())
	if !strings.Contains(err.Error(), "28P01") && !strings.Contains(err.Error(), "password authentication failed") {
		t.Errorf("the refusal is not an authentication failure: %v", err)
	}
	if proxy.Refused.Load() != before+1 {
		t.Error("the refusal was not the password check's")
	}
	// 3. The production opener with the old password is refused, without a secret in the text; with the new one it opens and serves.
	if _, err := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: dsn("reporting_app_t", appOld), InsecureTestOnly: true}); err == nil {
		t.Fatal("OpenProduction opened with the old password")
	} else {
		noSecret("OpenProduction's refusal with the old password", err.Error())
	}
	restarted, err := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: dsn("reporting_app_t", appNew), InsecureTestOnly: true})
	if err != nil {
		t.Fatal("OpenProduction with the new password:", err)
	}
	t.Cleanup(restarted.Close)
	if problems, err := postgres.RoleProblems(ctx, pool("reporting_app_t", appNew)); err != nil || len(problems) != 0 {
		t.Fatal("the role changed with its password", problems, err)
	}

	// 4. The consequence the runbook states: the running server, whose pool must open a connection after the server dropped its sessions
	// (a restart of the database, a network cut), cannot with the old password. Every answer it gives meanwhile is an error that names no
	// password, and the restarted one, with the new password, serves.
	terminate := func() {
		t.Helper()
		if _, err := admin.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = 's04h-rotation' AND pid <> pg_backend_pid()"); err != nil {
			t.Fatal(err)
		}
	}
	terminate()
	before = proxy.Refused.Load()
	failed := false
	for range 20 {
		code, body := get("jobs/" + jobID)
		noSecret("the running server's answer", body)
		if strings.Contains(strings.ToLower(body), "password") || strings.Contains(strings.ToLower(body), "scram") {
			t.Errorf("the answer names the credential: %q", body)
		}
		if code != 200 {
			failed = true
		}
		if proxy.Refused.Load() > before {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !failed || proxy.Refused.Load() <= before {
		t.Fatal("the running server did not meet the old password on a new connection", failed, proxy.Refused.Load()-before)
	}
	if code, body := get("jobs/" + jobID); code == 200 {
		t.Errorf("the running server serves with a connection the old password cannot open: %s", body)
	}
	if code, body := get("capabilities"); code != 200 {
		t.Error("/capabilities does not answer", code)
	} else {
		noSecret("/capabilities", body)
		var view map[string]any
		if json.Unmarshal([]byte(body), &view) != nil {
			t.Error("/capabilities is not JSON")
		}
	}
	freshService := issueDurableService(t, durableEnv{repo: restarted, source: env.source, p: env.p}, &stampIssuer{})
	if _, err := freshService.Job(ctx, env.p, jobID); err != nil {
		for range 3 { // the first use of a server's connections after the sessions were dropped may meet a dead one
			if _, err = freshService.Job(ctx, env.p, jobID); err == nil {
				break
			}
		}
		if err != nil {
			t.Fatal("the server restarted with the new password does not serve:", err)
		}
	}
	server.Close()

	// The sweep role has its own password and its own DSN: the same change, the same refusal, the same restart.
	setPassword("reporting_sweep_t", sweepOld)
	sweepOldPool := pool("reporting_sweep_t", sweepOld)
	if _, err := postgres.Sweep(ctx, sweepOldPool, r.SweepOptions{DryRun: true}); err != nil {
		t.Fatal("the sweep with its password:", err)
	}
	sweepHeld, err := sweepOldPool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sweepHeld.Release()
	setPassword("reporting_sweep_t", sweepNew)
	if err := sweepHeld.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		t.Fatal("the sweep's open connection stopped at the change", err)
	}
	spare, err := sweepOldPool.Acquire(ctx)
	if err == nil {
		spare.Release()
		t.Fatal("a new sweep connection with the old password was accepted")
	}
	noSecret("the sweep's refusal", err.Error())
	sweepNewPool := pool("reporting_sweep_t", sweepNew)
	if problems, err := postgres.SweeperProblems(ctx, sweepNewPool); err != nil || len(problems) != 0 {
		t.Fatal("the sweep role with the new password:", problems, err)
	}
	if _, err := postgres.Sweep(ctx, sweepNewPool, r.SweepOptions{DryRun: true}); err != nil {
		t.Fatal("the sweep with the new password:", err)
	}
	// The two roles' passwords are two secrets: one does not open the other's role.
	if _, err := pool("reporting_sweep_t", appNew).Acquire(ctx); err == nil {
		t.Error("the application role's password opened the sweep role")
	}
}
