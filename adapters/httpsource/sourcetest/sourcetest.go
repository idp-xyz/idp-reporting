// Package sourcetest is the reference source of the reporting source protocol (S19-A, docs/SOURCE_HTTP_PROTOCOL.md): an http.Handler that
// serves the protocol from any reporting.Adapter, from its reporting.DocumentAuthority when it has one, from its
// reporting.CompanyDirectory when it has one (S19-B) and from its reporting.DocumentCanceller when it has one (S24-A). The platform's tests run the
// contract simulators behind it, so that everything the platform does through adapters/httpsource is the same as what it does in process;
// a product team written in Go can read it as the shape of its own implementation. It is not a product: authority is the wrapped
// adapter's, and the handler only checks the service credential, decodes strictly and maps errors to the protocol's codes.
//
// The published filters and sorts (S20-A, ADR 0023) are the wrapped adapter's too: the catalog it answers carries the declaration the
// adapter makes, a read's query reaches the adapter's PrepareRead with its filters and sort as the platform sent them, and a filter or a
// sort the adapter's dataset version does not declare is the adapter's reporting.ErrUnsupported, answered here as `unsupported`. Behind
// the contract simulator (adapters/contractsim), which declares and applies them in memory, this is the reference source the checker
// (cmd/reporting-source-check) passes with every rule, F1–F6 included.
//
// The scope a read vouches for (S25-B2, ADR 0026 decision 2) is the wrapped adapter's as well: the read the handler answers carries
// scope_expires_at when the adapter's read declares one and not otherwise, a read sent back reaches the adapter with the scope the
// request carries, and whether that read is the one the source made, and until when it is authorized, the adapter decides by its own
// record (the simulator compares the whole read with the one it froze, and answers by its own clock). Behind a simulator that vouches
// (contractsim.Simulator.SetScopeLifetime) this is the reference source the checker passes with K1 and K2.
package sourcetest

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"idp.local/reporting/adapters/httpsource/protocol"
	r "idp.local/reporting/reporting"
)

// Options are the reference source's own settings.
type Options struct {
	// Token, when set, is the bearer credential every request must carry; a request without it is 401 without a body.
	Token string
	// RequireClientCertificate refuses (401) a request that did not present a verified client certificate (mTLS; the server's TLS
	// configuration verifies it).
	RequireClientCertificate bool
	// Observe, when set, is called with each request's method, path and headers before it is served (the tests read the headers the
	// platform sends).
	Observe func(*http.Request)
}

// New is the handler of the protocol over the adapter.
func New(a r.Adapter, o Options) http.Handler {
	h := &handler{a: a, o: o}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+protocol.PathCatalog, h.catalog)
	mux.HandleFunc("POST "+protocol.PathCompanies, h.companies)
	mux.HandleFunc("POST "+protocol.PathReads, h.prepare)
	mux.HandleFunc("POST "+protocol.PathPages, h.page)
	mux.HandleFunc("POST "+protocol.PathAuthorize, h.authorize)
	mux.HandleFunc("POST "+protocol.PathPrepare, h.prepareIssue)
	mux.HandleFunc("POST "+protocol.PathConfirm, h.confirm)
	mux.HandleFunc("POST "+protocol.PathVoid, h.void)
	mux.HandleFunc("POST "+protocol.PathCancellations, h.cancel)
	return h.guard(mux)
}

type handler struct {
	a r.Adapter
	o Options
}

func (h *handler) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if h.o.Observe != nil {
			h.o.Observe(req)
		}
		if h.o.Token != "" && subtle.ConstantTimeCompare([]byte(req.Header.Get("Authorization")), []byte("Bearer "+h.o.Token)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if h.o.RequireClientCertificate && (req.TLS == nil || len(req.TLS.VerifiedChains) == 0) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if id := req.Header.Get(protocol.RequestIDHeader); protocol.ValidJobID(id) {
			w.Header().Set(protocol.RequestIDHeader, id)
		}
		next.ServeHTTP(w, req)
	})
}

// decode reads a request body strictly; false has answered invalid.
func decode(w http.ResponseWriter, req *http.Request, out any) bool {
	mt, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err == nil && mt == "application/json" {
		b, rerr := io.ReadAll(io.LimitReader(req.Body, protocol.MaxRequestBytes+1))
		if rerr == nil && len(b) <= protocol.MaxRequestBytes && r.DecodeStrict(bytes.NewReader(b), out) == nil {
			return true
		}
	}
	refuse(w, protocol.CodeInvalid)
	return false
}

func refuse(w http.ResponseWriter, code string) {
	c, _ := protocol.Lookup(code)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(c.Status)
	_ = json.NewEncoder(w).Encode(protocol.ErrorBody{Code: c.Name})
}

func answer(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// failed answers the adapter's error: its code, 503 unavailable for a context that ended, 500 without a body for any other.
func failed(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		refuse(w, protocol.CodeUnavailable)
		return
	}
	if c, ok := protocol.CodeOf(err); ok {
		refuse(w, c.Name)
		return
	}
	w.WriteHeader(http.StatusInternalServerError)
}

// source is the request's context, which must be whole.
func source(w http.ResponseWriter, c *protocol.Context) (r.SourceContext, bool) {
	if c == nil || !c.Valid() {
		refuse(w, protocol.CodeInvalid)
		return r.SourceContext{}, false
	}
	return c.Source(), true
}

// read is the request's read, which must be whole and be the one the path names.
func read(w http.ResponseWriter, req *http.Request, sc r.SourceContext, wire *protocol.Read) (r.PreparedRead, bool) {
	if wire == nil || wire.Ref != req.PathValue("ref") {
		refuse(w, protocol.CodeInvalid)
		return r.PreparedRead{}, false
	}
	p, ok := wire.Prepared(sc)
	if !ok {
		refuse(w, protocol.CodeInvalid)
	}
	return p, ok
}

func (h *handler) catalog(w http.ResponseWriter, req *http.Request) {
	var in protocol.CatalogRequest
	if !decode(w, req, &in) {
		return
	}
	sc, ok := source(w, in.Context)
	if !ok {
		return
	}
	ds, err := h.a.DescribeCatalog(req.Context(), sc)
	if err != nil {
		failed(w, err)
		return
	}
	if ds == nil {
		ds = []r.Dataset{}
	}
	answer(w, protocol.CatalogResponse{Datasets: ds})
}

// companies is the company directory of ADR 0015 (S19-B): the wrapped adapter's list, with its names; a source without the capability
// answers unsupported (the platform never asks a connection that does not declare it).
func (h *handler) companies(w http.ResponseWriter, req *http.Request) {
	var in protocol.CompaniesRequest
	if !decode(w, req, &in) {
		return
	}
	sc, ok := source(w, in.Context)
	if !ok {
		return
	}
	d, ok := h.a.(r.CompanyDirectory)
	if !ok {
		refuse(w, protocol.CodeUnsupported)
		return
	}
	list, err := d.ListCompanies(req.Context(), sc)
	if err != nil {
		failed(w, err)
		return
	}
	if list.Companies == nil {
		list.Companies = []r.Company{}
	}
	partial := list.Partial
	answer(w, protocol.CompaniesResponse{Companies: list.Companies, Partial: &partial})
}

func (h *handler) prepare(w http.ResponseWriter, req *http.Request) {
	var in protocol.PrepareRequest
	if !decode(w, req, &in) {
		return
	}
	sc, ok := source(w, in.Context)
	if !ok {
		return
	}
	if in.Query == nil {
		refuse(w, protocol.CodeInvalid)
		return
	}
	p, err := h.a.PrepareRead(req.Context(), sc, *in.Query, in.QueryHash)
	if err != nil {
		failed(w, err)
		return
	}
	wire := protocol.ReadOf(p)
	answer(w, protocol.PrepareResponse{Read: &wire})
}

func (h *handler) page(w http.ResponseWriter, req *http.Request) {
	var in protocol.PageRequest
	if !decode(w, req, &in) {
		return
	}
	sc, ok := source(w, in.Context)
	if !ok {
		return
	}
	p, ok := read(w, req, sc, in.Read)
	if !ok {
		return
	}
	page, err := h.a.ReadPage(req.Context(), sc, p, in.Cursor, in.Limit)
	if err != nil {
		failed(w, err)
		return
	}
	if page.Rows == nil {
		page.Rows = [][]string{}
	}
	if page.Totals == nil {
		page.Totals = map[string]string{}
	}
	next := page.NextCursor
	answer(w, protocol.PageResponse{Ref: page.Ref, QueryHash: page.QueryHash, Watermark: page.Watermark, Rows: page.Rows, Totals: page.Totals, NextCursor: &next})
}

func (h *handler) authorize(w http.ResponseWriter, req *http.Request) {
	var in protocol.AuthorizeRequest
	if !decode(w, req, &in) {
		return
	}
	sc, ok := source(w, in.Context)
	if !ok {
		return
	}
	p, ok := read(w, req, sc, in.Read)
	if !ok {
		return
	}
	if err := h.a.AuthorizeCurrent(req.Context(), sc, p); err != nil {
		failed(w, err)
		return
	}
	yes := true
	answer(w, protocol.AuthorizeResponse{Authorized: &yes})
}

// authority is the wrapped adapter's document authority; a source without one answers the issue routes unsupported.
func (h *handler) authority(w http.ResponseWriter) (r.DocumentAuthority, bool) {
	a, ok := h.a.(r.DocumentAuthority)
	if !ok {
		refuse(w, protocol.CodeUnsupported)
	}
	return a, ok
}

func (h *handler) prepareIssue(w http.ResponseWriter, req *http.Request) {
	var in protocol.PrepareIssueRequest
	if !decode(w, req, &in) {
		return
	}
	sc, ok := source(w, in.Context)
	if !ok {
		return
	}
	if in.Intent == nil || in.Intent.JobID != req.PathValue("job_id") {
		refuse(w, protocol.CodeInvalid)
		return
	}
	intent, ok := in.Intent.IssueIntent(sc)
	if !ok {
		refuse(w, protocol.CodeInvalid)
		return
	}
	a, ok := h.authority(w)
	if !ok {
		return
	}
	if intent.Group != nil && !r.GroupIssueSupported(h.a) {
		refuse(w, protocol.CodeUnsupported)
		return
	}
	res, err := a.PrepareDocument(req.Context(), sc, intent)
	if err != nil {
		failed(w, err)
		return
	}
	answer(w, protocol.PrepareIssueResponse{Reservation: &protocol.Reservation{Ref: res.Ref, JobID: res.JobID, Number: res.Number, IssuedAt: protocol.FormatTime(res.IssuedAt), ExpiresAt: protocol.FormatTime(res.ExpiresAt)}})
}

func (h *handler) confirm(w http.ResponseWriter, req *http.Request) {
	var in protocol.ConfirmRequest
	if !decode(w, req, &in) {
		return
	}
	sc, ok := source(w, in.Context)
	if !ok {
		return
	}
	if in.Confirmation == nil || in.Confirmation.JobID != req.PathValue("job_id") {
		refuse(w, protocol.CodeInvalid)
		return
	}
	a, ok := h.authority(w)
	if !ok {
		return
	}
	c := in.Confirmation
	rec, err := a.ConfirmIssue(req.Context(), sc, r.IssueConfirmation{JobID: c.JobID, ReservationRef: c.ReservationRef, SHA256: c.SHA256, Pages: c.Pages})
	if err != nil {
		failed(w, err)
		return
	}
	answer(w, protocol.ConfirmResponse{Receipt: &protocol.Receipt{Ref: rec.Ref, ConfirmedAt: protocol.FormatTime(rec.ConfirmedAt)}})
}

func (h *handler) void(w http.ResponseWriter, req *http.Request) {
	var in protocol.VoidRequest
	if !decode(w, req, &in) {
		return
	}
	sc, ok := source(w, in.Context)
	if !ok {
		return
	}
	if in.JobID != req.PathValue("job_id") || len(in.Reason) > protocol.MaxReasonBytes {
		refuse(w, protocol.CodeInvalid)
		return
	}
	a, ok := h.authority(w)
	if !ok {
		return
	}
	if err := a.VoidIssue(req.Context(), sc, in.JobID, in.Reason); err != nil {
		failed(w, err)
		return
	}
	yes := true
	answer(w, protocol.VoidResponse{Voided: &yes})
}

// cancel is the cancellation of a confirmed document (ADR 0025 decision 1, S24-A): the wrapped adapter's decision, answered with the
// job it was asked about, its reference and its time. A request that is not of the contract's form is invalid; a source whose adapter
// cannot cancel answers unsupported (the platform never asks a connection that does not declare the capability). The adapter's
// refusals are the protocol's codes: denied, not_found for a job it never reserved, conflict for a document that is not a confirmed
// one or that the request names otherwise.
func (h *handler) cancel(w http.ResponseWriter, req *http.Request) {
	var in protocol.CancelRequest
	if !decode(w, req, &in) {
		return
	}
	sc, ok := source(w, in.Context)
	if !ok {
		return
	}
	if in.Cancellation == nil {
		refuse(w, protocol.CodeInvalid)
		return
	}
	c := in.Cancellation.IssueCancellation()
	if !c.Valid() {
		refuse(w, protocol.CodeInvalid)
		return
	}
	canceller, ok := h.a.(r.DocumentCanceller)
	if !ok {
		refuse(w, protocol.CodeUnsupported)
		return
	}
	receipt, err := canceller.CancelDocument(req.Context(), sc, c)
	if err != nil {
		failed(w, err)
		return
	}
	answer(w, protocol.CancelResponse{Receipt: &protocol.CancellationReceipt{JobID: c.JobID, Ref: receipt.Ref, CancelledAt: protocol.FormatTime(receipt.CancelledAt)}})
}
