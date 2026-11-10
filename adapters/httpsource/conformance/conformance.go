// Package conformance checks a source product's implementation of the reporting source protocol (S19-A, docs/SOURCE_HTTP_PROTOCOL.md)
// through the platform's own adapter, so that what passes here is what the platform accepts: cmd/reporting-source-check runs it against a
// product's endpoint. Every rule has a fixed id and text, and a failure says why in the adapter's fixed words; nothing a source answers is
// printed.
//
// The checks need a sample source context the product has granted (with at least one row in the sample query's scope) and the query.
// They only read; with Issue set they also reserve a number for a job id of their own and void it at once, which a product must be able to
// account for (do not set it against production).
package conformance

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"idp.local/reporting/adapters/httpsource"
	"idp.local/reporting/adapters/httpsource/protocol"
	r "idp.local/reporting/reporting"
)

// Result is one rule's outcome.
type Result struct {
	ID     string
	Rule   string
	Pass   bool
	Skip   bool
	Reason string // why it failed or was skipped: fixed text
}

// Line is the result as the command prints it.
func (x Result) Line() string {
	switch {
	case x.Skip:
		return "SKIP " + x.ID + " " + x.Rule + ": " + x.Reason
	case x.Pass:
		return "PASS " + x.ID + " " + x.Rule
	}
	return "FAIL " + x.ID + " " + x.Rule + ": " + x.Reason
}

// Target is what is checked.
type Target struct {
	Config  httpsource.Config // the connection as the platform would make it; its Issuance is ignored (Issue decides)
	Context r.SourceContext   // a context the product has granted
	Query   r.QuerySpec       // a query of the catalog, inside that grant, with at least one row
	Issue   bool              // also check the document authority (reserves and voids one number)
	// Client sends the raw requests of the rules that the adapter cannot make (a request without the credential); nil is a client with
	// the configuration's TLS settings.
	Client *http.Client
}

// Run checks every rule in order and returns the results. A rule whose precondition failed is skipped.
func Run(ctx context.Context, t Target) []Result {
	var out []Result
	add := func(id, rule string, err error) bool {
		x := Result{ID: id, Rule: rule, Pass: err == nil}
		if err != nil {
			x.Reason = reason(err)
		}
		out = append(out, x)
		return err == nil
	}
	skip := func(id, rule, why string) { out = append(out, Result{ID: id, Rule: rule, Skip: true, Reason: why}) }
	cfg := t.Config
	cfg.Issuance = httpsource.IssuanceNone
	if t.Issue {
		cfg.Issuance = httpsource.IssuanceGroups
	}
	a, err := httpsource.New(cfg)
	if !add("P1", "the connection is one the platform accepts (https or loopback http, a credential, the bounds)", err) {
		return out
	}
	defer a.(interface{ Close() }).Close()
	sc, q := t.Context, r.Normalize(t.Query)
	hash := r.QueryHash(q)

	// The credential.
	if cfg.Token != "" {
		add("A1", "a request without the service credential is refused with 401", unauthenticated(ctx, t, cfg, sc))
	} else {
		skip("A1", "a request without the service credential is refused with 401", "no bearer credential is configured")
	}

	// The catalog.
	ds, err := a.DescribeCatalog(ctx, sc)
	if !add("C1", "the catalog answers and every dataset verifies (columns are the dimensions then the metrics, typed and labelled)", err) {
		return out
	}
	var d *r.Dataset
	for i := range ds {
		if ds[i].Ref == q.Dataset {
			d = &ds[i]
		}
	}
	if d == nil {
		add("C2", "the sample query's dataset is in the catalog and the query fits it", fail("the dataset is not in the catalog", r.ErrUnsupported))
		return out
	}
	if !add("C2", "the sample query's dataset is in the catalog and the query fits it", r.ValidateQuery(q, *d)) {
		return out
	}
	stranger := sc
	stranger.Owner.Subject = "reporting-source-check-" + r.NewID()
	_, err = a.DescribeCatalog(ctx, stranger)
	add("C3", "a subject the product does not know is refused (denied)", expect(err, r.ErrDenied))
	otherTenant := sc
	otherTenant.SourceTenant = "reporting-source-check-" + r.NewID()
	_, err = a.DescribeCatalog(ctx, otherTenant)
	add("C4", "a source tenant that is not the connection's is refused (denied)", expect(err, r.ErrDenied))

	// The read.
	p, err := a.PrepareRead(ctx, sc, q, hash)
	if !add("R1", "a read is prepared for the query and verifies (hash, dataset, companies, zone, policy, watermark, expiry within 15 minutes)", err) {
		return out
	}
	add("R2", "the read is authorized now", a.AuthorizeCurrent(ctx, sc, p))
	rows, totals, err := readAll(ctx, a, sc, p)
	if add("R3", "every page verifies (reference, hash, watermark, row width and types, totals, cursor at most 2048 and moving) within 1000 rows", err) {
		again, againTotals, err := readAll(ctx, a, sc, p)
		if err == nil && (!reflect.DeepEqual(rows, again) || !reflect.DeepEqual(totals, againTotals)) {
			err = fail("a second read of the same read gave other rows or totals", r.ErrIntegrity)
		}
		add("R4", "reading the read again gives the same rows and totals (a stable snapshot)", err)
		var none error
		if len(rows) == 0 {
			none = fail("the read has no row", r.ErrInvalid)
		}
		add("R5", "the sample query has at least one row (a check of an empty read proves little)", none)
	}
	_, err = a.ReadPage(ctx, sc, p, "reporting-source-check-not-a-cursor", r.SourcePageSize)
	add("R6", "a cursor the source never gave is refused (invalid)", expect(err, r.ErrInvalid))
	forged := p
	forged.Watermark += "-forged"
	add("R7", "a read with another watermark is refused (denied or expired)", expect(a.AuthorizeCurrent(ctx, sc, forged), r.ErrDenied))
	forged = p
	forged.ScopeHandle += "-forged"
	add("R8", "a read with another scope handle is refused (denied or expired)", expect(a.AuthorizeCurrent(ctx, sc, forged), r.ErrDenied))
	unknown := p
	unknown.Ref = r.NewID()
	add("R9", "a read the source never made is refused (denied or expired)", expect(a.AuthorizeCurrent(ctx, sc, unknown), r.ErrDenied))
	theirs := p
	theirs.Context = stranger
	add("R10", "the read of the sample subject is refused to another subject (denied)", expect(a.AuthorizeCurrent(ctx, stranger, theirs), r.ErrDenied))
	_, err = a.PrepareRead(ctx, sc, q, strings.Repeat("0", 64))
	add("R11", "a query hash that is not the query's is refused (integrity or invalid)", expect(err, r.ErrIntegrity, r.ErrInvalid))
	outside := q
	outside.Companies = []string{"reporting-source-check-" + r.NewID()}
	_, err = a.PrepareRead(ctx, sc, outside, r.QueryHash(outside))
	add("R12", "a company outside the subject's grant is refused (denied)", expect(err, r.ErrDenied))
	version := q
	version.Dataset.Version += "-reporting-source-check"
	_, err = a.PrepareRead(ctx, sc, version, r.QueryHash(version))
	add("R13", "a dataset version that is not in the catalog is refused (unsupported or invalid)", expect(err, r.ErrUnsupported, r.ErrInvalid))

	// The document authority.
	const issueRule = "a number is reserved for one row, the same intent answers the same reservation, a void is final"
	if !t.Issue {
		skip("I1", issueRule, "not asked (-issue reserves and voids one number)")
		return out
	}
	if len(rows) == 0 {
		skip("I1", issueRule, "the sample query has no row")
		return out
	}
	add("I1", issueRule, issue(ctx, a.(r.DocumentAuthority), sc, p, rows[0]))
	return out
}

func issue(ctx context.Context, auth r.DocumentAuthority, sc r.SourceContext, p r.PreparedRead, row []string) error {
	job := r.NewID()
	in := r.IssueIntent{JobID: job, Prepared: p, RowIndex: 0, RowSHA256: r.Digest(row), TemplateID: r.NewID(), TemplateVersion: 1, TemplateSHA256: strings.Repeat("0", 64)}
	res, err := auth.PrepareDocument(ctx, sc, in)
	if err != nil {
		return err
	}
	again, err := auth.PrepareDocument(ctx, sc, in)
	if err != nil {
		return err
	}
	if again.Ref != res.Ref || again.Number != res.Number || !again.IssuedAt.Equal(res.IssuedAt) {
		return fail("the same intent answered another reservation", r.ErrIntegrity)
	}
	other := in
	other.TemplateVersion = 2
	if _, err = auth.PrepareDocument(ctx, sc, other); !errors.Is(err, r.ErrConflict) {
		return fail("another intent under the same job id was not a conflict", unexpected(err))
	}
	if err = auth.VoidIssue(ctx, sc, job, "reporting-source-check"); err != nil {
		return err
	}
	if err = auth.VoidIssue(ctx, sc, job, "reporting-source-check"); err != nil {
		return fail("voiding twice was refused", err)
	}
	if _, err = auth.PrepareDocument(ctx, sc, in); !errors.Is(err, r.ErrConflict) {
		return fail("a voided job could be reserved again", unexpected(err))
	}
	return nil
}

// readAll reads the whole read page after page, as the service does: the totals equal on every page, at most 1000 rows.
func readAll(ctx context.Context, a r.Adapter, sc r.SourceContext, p r.PreparedRead) ([][]string, map[string]string, error) {
	var rows [][]string
	var totals map[string]string
	cursor := ""
	seen := map[string]bool{}
	for n := 0; ; n++ {
		page, err := a.ReadPage(ctx, sc, p, cursor, r.SourcePageSize)
		if err != nil {
			return nil, nil, err
		}
		if n == 0 {
			totals = page.Totals
		} else if !reflect.DeepEqual(totals, page.Totals) {
			return nil, nil, fail("the totals changed from one page to the next", r.ErrIntegrity)
		}
		rows = append(rows, page.Rows...)
		if len(rows) > r.MaxRows {
			return nil, nil, fail("the read has more than 1000 rows", r.ErrBudget)
		}
		if page.NextCursor == "" {
			return rows, totals, nil
		}
		if seen[page.NextCursor] {
			return nil, nil, fail("a cursor came back", r.ErrIntegrity)
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
}

// unauthenticated sends the catalog request without the credential and expects 401.
func unauthenticated(ctx context.Context, t Target, cfg httpsource.Config, sc r.SourceContext) error {
	client := t.Client
	if client == nil {
		client = clientOf(cfg)
	}
	wire := protocol.ContextOf(sc)
	b, _ := json.Marshal(protocol.CatalogRequest{Context: &wire})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(cfg.BaseURL, "/")+protocol.PathCatalog, bytes.NewReader(b))
	if err != nil {
		return errors.New("the request cannot be made")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(protocol.RequestIDHeader, r.NewID())
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("the source could not be reached")
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return fmt.Errorf("the source answered status %d", resp.StatusCode)
	}
	return nil
}

// clientOf is a client with the connection's TLS settings that follows no redirect.
func clientOf(cfg httpsource.Config) *http.Client {
	tlsc := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: cfg.RootCAs}
	if cfg.Certificate != nil {
		tlsc.Certificates = []tls.Certificate{*cfg.Certificate}
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = httpsource.DefaultTimeout
	}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: tlsc}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// checkError is a rule's own failure.
type checkError struct {
	why string
	err error
}

func (e *checkError) Error() string { return e.why + ": " + e.err.Error() }
func (e *checkError) Unwrap() error { return e.err }

func fail(why string, err error) error { return &checkError{why, err} }

// unexpected names what came instead of the expected refusal.
func unexpected(err error) error {
	if err == nil {
		return errors.New("the source accepted it")
	}
	return err
}

// expect is nil when err is one of the wanted platform errors.
func expect(err error, wanted ...error) error {
	for _, w := range wanted {
		if errors.Is(err, w) {
			return nil
		}
	}
	names := make([]string, len(wanted))
	for i, w := range wanted {
		names[i] = w.Error()
	}
	if err == nil {
		return fmt.Errorf("the source accepted it; want %s", strings.Join(names, " or "))
	}
	return fmt.Errorf("want %s, got: %w", strings.Join(names, " or "), err)
}

// reason is the fixed text of a failure: the adapter's and the rules' errors are fixed texts already.
func reason(err error) string { return err.Error() }
