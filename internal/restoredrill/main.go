// restoredrill is the Go side of scripts/restore_drill.sh (S04-F, docs/S04F_SCOPE.md F4): the local evidence that a platform restored from a
// pg_dump and a copy of its bucket is what docs/OPERATIONS.md "Backup and restore" says. It is not part of the platform and not an API: a
// small server that wires what the demo wires onto the drill's scratch databases (which the demo's guard refuses, as it refuses every
// database but the fixtures), a client of that server, and the copy of the bucket's objects through the platform's own object store client.
//
// It opens only databases named with fixturedb.DrillPrefix on a loopback address and never port 55432, the product simulator that outlives the
// platform process the drill kills (pgsource.OpenDrill) included. The platform store is opened through the production path as the application
// role, as a deployment's server would be. The issuer is the test suites' stamp issuer: a PDF made of the stamp, no Typst.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"idp.local/reporting/adapters/contractsim"
	"idp.local/reporting/adapters/contractsim/pgsource"
	"idp.local/reporting/export/xlsx"
	"idp.local/reporting/internal/fixturedb"
	r "idp.local/reporting/reporting"
	"idp.local/reporting/storage/objects"
	"idp.local/reporting/storage/postgres"
	"idp.local/reporting/transport/httpapi"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "restoredrill:", err)
		os.Exit(1)
	}
}

const usage = `usage: restoredrill <init-source|serve|session|fetch|issue|wait-job|product|bucket> ...
the environment: DRILL_PLATFORM_DSN (the application role), DRILL_ADMIN_DSN (a superuser, for the product and the rows), DRILL_SOURCE_DSN,
DRILL_TOKEN, DRILL_URL, DRILL_STALL_FILE, and REPORTING_OBJECT_STORE_ACCESS_KEY / _SECRET_KEY with the object store flags`

func run(args []string, out io.Writer) error {
	if len(args) < 1 {
		return errors.New(usage)
	}
	if err := objects.RefuseCredentialFlags(args[1:]); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(out)
	store := objects.Register(fs)
	listen := fs.String("listen", "127.0.0.1:7811", "serve: loopback address")
	state := fs.String("state", "", "session, fetch: the JSON file of the artifacts a session made")
	label := fs.String("label", "", "session: names the keys and the lines of this session")
	id := fs.String("id", "", "wait-job, product, issue: a job id (issue: the snapshot id)")
	timeout := fs.Duration("timeout", 90*time.Second, "wait-job: how long to wait")
	dir := fs.String("dir", "", "bucket: the directory the copy is in")
	// bucket takes its action first, then its flags.
	rest := args[1:]
	action := ""
	if args[0] == "bucket" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		action, rest = rest[0], rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return err
	}
	switch args[0] {
	case "init-source":
		return initSource(ctx, out)
	case "serve":
		return serve(ctx, out, store, *listen)
	case "session":
		return withClient(ctx, out, func(c *client) error { return c.session(*state, *label) })
	case "fetch":
		return withClient(ctx, out, func(c *client) error { return c.fetch(*state) })
	case "issue":
		return withClient(ctx, out, func(c *client) error { return c.issueStart(*id, *label) })
	case "wait-job":
		return withClient(ctx, out, func(c *client) error { return c.waitJob(*id, *timeout) })
	case "product":
		return product(ctx, out, *id)
	case "bucket":
		return bucket(ctx, out, store, action, *dir)
	}
	return errors.New(usage)
}

// need reads a variable of the environment.
func need(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("set %s", name)
	}
	return v, nil
}

// The principal and the frozen template are the demo's and the test suites'.
func principal() r.Principal {
	p := contractsim.DemoPrincipal()
	p.Integrations = append(p.Integrations, contractsim.ShipmentIntegration)
	return p
}

const frozenTemplate = `{"schema_version":"1.0","kind":"document","binding":{"integration_id":"i","dataset":{"id":"d","version":"1"}},"elements":[]}`

func issueInput(snapshot string, row int) r.IssueInput {
	return r.IssueInput{TemplateID: strings.Repeat("a", 32), TemplateVersion: 1, Template: []byte(frozenTemplate), SnapshotID: snapshot, RowIndex: row}
}

// initSource creates the product simulator's schema in its scratch database and seeds the two integrations and the demo's grants.
func initSource(ctx context.Context, out io.Writer) error {
	dsn, err := need("DRILL_SOURCE_DSN")
	if err != nil {
		return err
	}
	src, err := pgsource.OpenDrill(ctx, dsn, true)
	if err != nil {
		return err
	}
	defer src.Close()
	p := principal()
	for i, seed := range []contractsim.Fixture{contractsim.Financial().Fixture(), contractsim.Shipment().Fixture()} {
		id := []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration}[i]
		if err = src.EnsureFixture(ctx, id, "tenant_a", seed); err != nil {
			return err
		}
		g := contractsim.DefaultGrant()
		if i == 1 {
			g.CurrencyPolicy = nil
		}
		if err = src.EnsureGrant(ctx, r.SourceContext{Owner: contractsim.DemoOwner(p, id), SourceTenant: "tenant_a"}, g); err != nil {
			return err
		}
	}
	fmt.Fprintln(out, "the product simulator's scratch database is initialized")
	return nil
}

// stampIssuer renders a PDF from the request alone, as the real issuer does (the same request gives the same bytes). While the file named by
// DRILL_STALL_FILE exists it stops inside the render, after the product reserved the number and before it confirmed the file, and says so by
// creating the file's ".hit": the drill kills the process there.
type stampIssuer struct{ stall string }

func (stampIssuer) Check(context.Context, r.Job, r.Snapshot) error { return nil }
func (s stampIssuer) Issue(ctx context.Context, req r.IssueRequest) (r.IssueResult, error) {
	if s.stall != "" {
		if _, err := os.Stat(s.stall); err == nil {
			_ = os.WriteFile(s.stall+".hit", []byte(req.Job.ID+"\n"), 0o600)
			for {
				select {
				case <-ctx.Done():
					return r.IssueResult{}, ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
	}
	return r.IssueResult{PDF: []byte("%PDF-1.7\n" + req.Stamp + "\n" + req.At.Format(time.RFC3339) + "\n%%EOF"), Pages: 1}, nil
}

// stubPDF is the writer the service wants registered for the pdf format before it admits an issue job; the drill exports workbooks and issues
// through the stamp issuer, so it is never used for a table export.
type stubPDF struct{}

func (stubPDF) Write(context.Context, r.Snapshot) ([]byte, error) { return []byte("%PDF-1.7 x"), nil }

func serve(ctx context.Context, out io.Writer, flags *objects.Flags, listen string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("loopback listen address required")
	}
	platform, err := need("DRILL_PLATFORM_DSN")
	if err != nil {
		return err
	}
	sourceDSN, err := need("DRILL_SOURCE_DSN")
	if err != nil {
		return err
	}
	token, err := need("DRILL_TOKEN")
	if err != nil {
		return err
	}
	client, err := flags.Client(os.Getenv)
	if err != nil || client == nil {
		return errors.New("the drill needs the object store flags: it exists to qualify the platform with one")
	}
	if err = objects.CheckAtStart(ctx, client); err != nil {
		return err
	}
	// The drill's platform database is guarded like the source's, here by the name in the DSN, before the production path opens it.
	if pool, e := fixturedb.OpenDrill(ctx, platform); e != nil {
		return fmt.Errorf("DRILL_PLATFORM_DSN is not a scratch database of the drill: %w", e)
	} else {
		pool.Close()
	}
	repo, err := postgres.OpenProduction(ctx, postgres.ProductionConfig{DSN: platform, InsecureTestOnly: true}, postgres.WithObjects(client))
	if err != nil {
		return fmt.Errorf("the restored store does not open as a production store: %w", err)
	}
	defer repo.Close()
	src, err := pgsource.OpenDrill(ctx, sourceDSN, false)
	if err != nil {
		return err
	}
	defer src.Close()
	p := principal()
	cs := []r.Connection{}
	for _, id := range []string{contractsim.FinanceIntegration, contractsim.ShipmentIntegration} {
		cs = append(cs, r.Connection{Tenant: p.Tenant, Integration: id, SourceTenant: "tenant_a", Adapter: src})
	}
	key := sha256.Sum256([]byte("restore-drill-cursor:" + token))
	service, err := r.NewService(r.Config{Repository: repo, Connections: cs, CursorKey: key[:], Writer: xlsx.Writer{}, Formats: map[string]r.WorkbookWriter{"pdf": stubPDF{}}, Issuer: stampIssuer{stall: os.Getenv("DRILL_STALL_FILE")}, Objects: client})
	if err != nil {
		return err
	}
	auth, err := httpapi.DemoBearer(token, p)
	if err != nil {
		return err
	}
	api, err := httpapi.Handler(service, auth)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/", api)
	// The one route the drill adds: an issue job for row 0 of a snapshot, because the demo's own route needs a template store and Typst.
	mux.HandleFunc("POST /drill/issue", func(w http.ResponseWriter, req *http.Request) {
		who, e := auth(req)
		if e != nil {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		var body struct{ SnapshotID string }
		if e = json.NewDecoder(io.LimitReader(req.Body, 4096)).Decode(&body); e != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		job, e := service.SubmitIssue(req.Context(), who, issueInput(body.SnapshotID, 0), req.Header.Get("Idempotency-Key"))
		if e != nil {
			http.Error(w, e.Error(), http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(job)
	})
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 25 * time.Second, WriteTimeout: 25 * time.Second}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if _, e := service.RunOne(ctx); e != nil && !errors.Is(e, context.Canceled) {
					fmt.Fprintln(out, "worker: a job stopped:", e)
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	fmt.Fprintf(out, "restoredrill serve: pid=%d listening at http://%s (production store as the application role; objects in bucket %q)\n", os.Getpid(), listener.Addr(), client.Bucket)
	err = server.Serve(listener)
	workers.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// client is the drill's HTTP client of the server above.
type client struct {
	ctx   context.Context
	out   io.Writer
	base  string
	token string
	http  *http.Client
}

func withClient(ctx context.Context, out io.Writer, fn func(*client) error) error {
	base, err := need("DRILL_URL")
	if err != nil {
		return err
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		return errors.New("DRILL_URL must be an http loopback address")
	}
	token, err := need("DRILL_TOKEN")
	if err != nil {
		return err
	}
	return fn(&client{ctx: ctx, out: out, base: strings.TrimRight(base, "/"), token: token, http: &http.Client{Timeout: 25 * time.Second}})
}

func (c *client) call(method, path string, payload any, key string) (int, http.Header, []byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(c.ctx, method, c.base+path, body)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, r.MaxArtifactBytes+1))
	return resp.StatusCode, resp.Header, b, err
}

const api = "/api/reporting/v1"

func (c *client) submit(path string, payload any, key string) (r.JobView, error) {
	code, _, b, err := c.call("POST", path, payload, key)
	if err != nil {
		return r.JobView{}, err
	}
	var j r.JobView
	if code != 202 || json.Unmarshal(b, &j) != nil {
		return j, fmt.Errorf("POST %s answered %d %s", path, code, strings.TrimSpace(string(b[:min(len(b), 200)])))
	}
	return j, nil
}

func (c *client) job(id string) (r.JobView, error) {
	code, _, b, err := c.call("GET", api+"/jobs/"+id, nil, "")
	if err != nil {
		return r.JobView{}, err
	}
	var j r.JobView
	if code != 200 || json.Unmarshal(b, &j) != nil {
		return j, fmt.Errorf("GET job answered %d", code)
	}
	return j, nil
}

// waitJob polls the job and says each change of its state, with the time, until it has succeeded.
func (c *client) waitJob(id string, limit time.Duration) error {
	_, err := c.wait(id, limit, true)
	return err
}

func (c *client) wait(id string, limit time.Duration, say bool) (r.JobView, error) {
	end := time.Now().Add(limit)
	last := ""
	for {
		j, err := c.job(id)
		if err != nil {
			return j, err
		}
		if say && j.State != last {
			fmt.Fprintf(c.out, "%s job %s is %s\n", time.Now().UTC().Format("15:04:05.000"), id, j.State)
		}
		last = j.State
		switch j.State {
		case "succeeded":
			return j, nil
		case "failed", "cancelled":
			return j, fmt.Errorf("job %s is %s (%s)", id, j.State, j.ErrorCode)
		}
		if time.Now().After(end) {
			return j, fmt.Errorf("job %s is still %s after %s", id, j.State, limit)
		}
		select {
		case <-c.ctx.Done():
			return j, c.ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// record is one artifact a session made, with the digest the server served it with.
type record struct {
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Job      string `json:"job"`
	Snapshot string `json:"snapshot"`
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
	Bytes    int    `json:"bytes"`
	Number   string `json:"number,omitempty"`
}

func readState(path string) ([]record, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rs []record
	return rs, json.Unmarshal(b, &rs)
}

func (c *client) download(id string) (int, string, []byte, error) {
	code, h, b, err := c.call("GET", api+"/artifacts/"+id+"/content", nil, "")
	if err != nil {
		return 0, "", nil, err
	}
	if code != 200 {
		var e struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(b, &e)
		return code, e.Code, nil, nil
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != h.Get("X-Content-SHA256") {
		return code, "BODY_DIGEST_DIFFERS_FROM_HEADER", b, nil
	}
	return code, "", b, nil
}

// session makes a snapshot, an export and an issue, downloads both files and appends what it made to the state file.
func (c *client) session(statePath, label string) error {
	if statePath == "" || label == "" {
		return errors.New("session needs -state and -label")
	}
	rs, err := readState(statePath)
	if err != nil {
		return err
	}
	sj, err := c.submit(api+"/snapshot-jobs", contractsim.FinancialQuery(), label+"-snapshot")
	if err != nil {
		return err
	}
	if sj, err = c.wait(sj.ID, 60*time.Second, false); err != nil {
		return err
	}
	ej, err := c.submit(api+"/exports", map[string]string{"snapshot_id": sj.SnapshotID}, label+"-export")
	if err != nil {
		return err
	}
	ij, err := c.submitIssue(sj.SnapshotID, label+"-issue")
	if err != nil {
		return err
	}
	for _, j := range []struct {
		kind string
		job  r.JobView
	}{{"export", ej}, {"issue", ij}} {
		done, err := c.wait(j.job.ID, 60*time.Second, false)
		if err != nil {
			return err
		}
		code, why, body, err := c.download(done.ArtifactID)
		if err != nil || code != 200 || why != "" {
			return fmt.Errorf("download of the new %s: %d %s %v", j.kind, code, why, err)
		}
		sum := sha256.Sum256(body)
		rec := record{Label: label, Kind: j.kind, Job: done.ID, Snapshot: sj.SnapshotID, Artifact: done.ArtifactID, SHA256: hex.EncodeToString(sum[:]), Bytes: len(body)}
		if done.Issue != nil {
			rec.Number = done.Issue.Number
		}
		rs = append(rs, rec)
		fmt.Fprintf(c.out, "session %s: %s job %s -> artifact %s, %d bytes, sha256 %.16s..., downloaded 200%s\n", label, j.kind, done.ID, done.ArtifactID, len(body), rec.SHA256, numberNote(rec.Number))
	}
	b, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath, append(b, '\n'), 0o600)
}

func numberNote(n string) string {
	if n == "" {
		return ""
	}
	return ", issue number " + n
}

func (c *client) submitIssue(snapshot, key string) (r.JobView, error) {
	return c.submit("/drill/issue", map[string]string{"SnapshotID": snapshot}, key)
}

// issueStart admits an issue job for row 0 of the snapshot (a new snapshot when none is named: a source read lives ten minutes) and says its id.
func (c *client) issueStart(snapshot, key string) error {
	if snapshot == "" {
		sj, err := c.submit(api+"/snapshot-jobs", contractsim.FinancialQuery(), key+"-snapshot")
		if err != nil {
			return err
		}
		if sj, err = c.wait(sj.ID, 60*time.Second, false); err != nil {
			return err
		}
		snapshot = sj.SnapshotID
	}
	j, err := c.submitIssue(snapshot, key)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "issue job %s admitted (%s) on snapshot %s\n", j.ID, j.State, snapshot)
	return nil
}

// fetch downloads every artifact of the state file and says what each answered: 200 with the digest the session saw, or the status and the code.
func (c *client) fetch(statePath string) error {
	rs, err := readState(statePath)
	if err != nil || len(rs) == 0 {
		return fmt.Errorf("no records in %s: %v", statePath, err)
	}
	tally := map[string]int{}
	for _, rec := range rs {
		code, why, body, err := c.download(rec.Artifact)
		if err != nil {
			return err
		}
		verdict := fmt.Sprintf("%d %s", code, why)
		if code == 200 {
			sum := sha256.Sum256(body)
			if hex.EncodeToString(sum[:]) == rec.SHA256 {
				verdict = "200 the bytes the session saw"
			} else {
				verdict = "200 BUT NOT THE BYTES THE SESSION SAW"
			}
		}
		tally[strconv.Itoa(code)]++
		fmt.Fprintf(c.out, "download %-6s %-6s artifact %s: %s\n", rec.Label, rec.Kind, rec.Artifact, strings.TrimSpace(verdict))
	}
	fmt.Fprintf(c.out, "downloads: %v\n", tally)
	return nil
}

// product says what the product simulator holds for a job id.
func product(ctx context.Context, out io.Writer, jobID string) error {
	dsn, err := need("DRILL_SOURCE_DSN")
	if err != nil {
		return err
	}
	src, err := pgsource.OpenDrill(ctx, dsn, false)
	if err != nil {
		return err
	}
	defer src.Close()
	rec, ok, err := src.IssueRecord(ctx, jobID)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(out, "product: no record for job %s\n", jobID)
		return nil
	}
	fmt.Fprintf(out, "product: job %s is %s, number %s, reservation %.12s, confirmation %.12s\n", jobID, rec.State, rec.Number, rec.Ref, rec.ConfirmationRef)
	return nil
}

// manifest is the copy of the bucket's objects: what the rows name, with the digest and length the client verifies each against.
type manifestEntry struct {
	Key    string `json:"key"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

func bucket(ctx context.Context, out io.Writer, flags *objects.Flags, action, dir string) error {
	s3, err := flags.Client(os.Getenv)
	if err != nil || s3 == nil {
		return errors.New("bucket needs the object store flags")
	}
	if s3.Bucket != "reporting-restore-s04f" {
		return fmt.Errorf("the drill touches only its own bucket, reporting-restore-s04f, not %q", s3.Bucket)
	}
	list := func() ([]string, error) {
		var keys []string
		after := ""
		for {
			page, next, err := s3.List(ctx, r.ObjectPrefix, after, 1000)
			if err != nil {
				return nil, err
			}
			for _, o := range page {
				keys = append(keys, o.Key)
			}
			if next == "" {
				return keys, nil
			}
			after = next
		}
	}
	switch action {
	case "count":
		keys, err := list()
		fmt.Fprintf(out, "bucket %s holds %d objects under %s\n", s3.Bucket, len(keys), r.ObjectPrefix)
		return err
	case "purge":
		keys, err := list()
		if err != nil {
			return err
		}
		for _, k := range keys {
			if err = s3.Delete(ctx, k); err != nil {
				return err
			}
		}
		fmt.Fprintf(out, "bucket %s: %d objects removed\n", s3.Bucket, len(keys))
		return nil
	case "copy":
		// The rows name the objects (and their digests and lengths): the copy is each object read back through the client, which refuses a
		// body that is not what the row says, as a backup of the bucket at the moment of the dump would hold.
		admin, err := need("DRILL_ADMIN_DSN")
		if err != nil {
			return err
		}
		pool, err := fixturedb.OpenDrill(ctx, admin)
		if err != nil {
			return err
		}
		defer pool.Close()
		rows, err := pool.Query(ctx, `SELECT data->>'Object', data->>'SHA256', (data->>'Size')::int FROM reporting_s01.assets WHERE kind='artifact' AND coalesce(data->>'Object','') <> '' ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		if err = os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		var manifest []manifestEntry
		for rows.Next() {
			var e manifestEntry
			if err = rows.Scan(&e.Key, &e.SHA256, &e.Size); err != nil {
				return err
			}
			body, err := s3.Get(ctx, e.Key, e.SHA256, e.Size)
			if err != nil {
				return err
			}
			e.File = strings.TrimPrefix(e.Key, r.ObjectPrefix)
			if err = os.WriteFile(filepath.Join(dir, e.File), body, 0o600); err != nil {
				return err
			}
			manifest = append(manifest, e)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		b, _ := json.MarshalIndent(manifest, "", "  ")
		fmt.Fprintf(out, "bucket %s: %d objects copied aside\n", s3.Bucket, len(manifest))
		return os.WriteFile(filepath.Join(dir, "manifest.json"), append(b, '\n'), 0o600)
	case "restore":
		b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			return err
		}
		var manifest []manifestEntry
		if err = json.Unmarshal(b, &manifest); err != nil {
			return err
		}
		for _, e := range manifest {
			body, err := os.ReadFile(filepath.Join(dir, e.File))
			if err != nil {
				return err
			}
			if err = s3.Put(ctx, e.Key, body, e.SHA256); err != nil {
				return err
			}
		}
		fmt.Fprintf(out, "bucket %s: %d objects put back from the copy\n", s3.Bucket, len(manifest))
		return nil
	}
	return errors.New("bucket copy|restore|purge|count")
}
