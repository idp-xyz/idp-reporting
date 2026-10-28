// Package contractsim provides explicit synthetic product-side contracts.
// No real Brant/Parcel API or database is queried by this package.
package contractsim

import (
	"context"
	"encoding/json"
	r "idp.local/reporting/reporting"
	"math/big"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Record struct {
	Company string
	Key     string
	Values  []string
}
type Grant struct {
	Companies      []string
	Rows           []string
	Timezone       string
	CurrencyPolicy *r.Ref
	// IssueDocuments lets the principal issue formal documents (S03-H). A grant stored without
	// it, or no grant at all, does not.
	IssueDocuments bool
}
type frozen struct {
	prepared r.PreparedRead
	query    r.QuerySpec
	grant    Grant
	records  []Record
}
type Simulator struct {
	mu          sync.Mutex
	dataset     r.Dataset
	records     []Record
	grants      map[r.Owner]Grant
	frozen      map[string]frozen
	unavailable bool
	reads       int
	revision    int
	// The product-authority capability (authority.go): the number prefix, the last number given
	// out and the record of every job per source, the approval switch and the clock.
	prefix       string
	numbers      map[source]int64
	issues       map[issueKey]IssueRecord
	issueRefused bool
	clock        func() time.Time
}

func copyValue[T any](v T) T {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	var out T
	if e = json.Unmarshal(b, &out); e != nil {
		panic(e)
	}
	return out
}
func New(d r.Dataset, records []Record) *Simulator {
	return &Simulator{dataset: copyValue(d), records: copyValue(records), grants: map[r.Owner]Grant{}, frozen: map[string]frozen{}, revision: 1, prefix: DefaultNumberPrefix, numbers: map[source]int64{}, issues: map[issueKey]IssueRecord{}}
}

// Explicit fixture controls are Go-only. They are never exposed as HTTP admin
// endpoints and cannot be used to mutate a production source.
func (s *Simulator) Grant(o r.Owner, g Grant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grants[o] = copyValue(g)
}
func (s *Simulator) Revoke(o r.Owner)      { s.mu.Lock(); defer s.mu.Unlock(); delete(s.grants, o) }
func (s *Simulator) SetUnavailable(v bool) { s.mu.Lock(); defer s.mu.Unlock(); s.unavailable = v }
func (s *Simulator) ReplaceRecords(v []Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = copyValue(v)
	s.revision++
}
func (s *Simulator) ReadCalls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.reads }
func (s *Simulator) catalog(sc r.SourceContext) (Grant, error) {
	if s.unavailable {
		return Grant{}, r.ErrUnavailable
	}
	g, ok := s.grants[sc.Owner]
	if !ok || sc.SourceTenant != "tenant_a" {
		return Grant{}, r.ErrDenied
	}
	return g, nil
}
func (s *Simulator) DescribeCatalog(ctx context.Context, sc r.SourceContext) ([]r.Dataset, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, e := s.catalog(sc); e != nil {
		return nil, e
	}
	return []r.Dataset{copyValue(s.dataset)}, nil
}
func (s *Simulator) PrepareRead(ctx context.Context, sc r.SourceContext, q r.QuerySpec, hash string) (r.PreparedRead, error) {
	if e := ctx.Err(); e != nil {
		return r.PreparedRead{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, e := s.catalog(sc)
	if e != nil {
		return r.PreparedRead{}, e
	}
	if e = r.ValidateQuery(q, s.dataset); e != nil {
		return r.PreparedRead{}, e
	}
	if q.Integration != sc.Owner.Integration || hash != r.QueryHash(q) {
		return r.PreparedRead{}, r.ErrIntegrity
	}
	for _, c := range q.Companies {
		if !slices.Contains(g.Companies, c) {
			return r.PreparedRead{}, r.ErrDenied
		}
	}
	if g.Timezone != q.Timezone || !reflect.DeepEqual(g.CurrencyPolicy, q.CurrencyPolicy) {
		return r.PreparedRead{}, r.ErrDenied
	}
	now := s.now()
	for id, v := range s.frozen {
		if !now.Before(v.prepared.ExpiresAt) {
			delete(s.frozen, id)
		}
	}
	if len(s.frozen) >= 10000 {
		return r.PreparedRead{}, r.ErrBudget
	}
	p := r.PreparedRead{Context: sc, Ref: r.NewID(), ScopeHandle: r.NewID(), QueryHash: hash, Dataset: q.Dataset, Companies: slices.Clone(q.Companies), Timezone: q.Timezone, CurrencyPolicy: copyValue(q.CurrencyPolicy), Watermark: "fixture-v" + strconv.Itoa(s.revision), ExpiresAt: now.Add(10 * time.Minute)}
	// Immutable version pointer; admission does not enumerate or query rows.
	s.frozen[p.Ref] = frozen{prepared: copyValue(p), query: copyValue(q), grant: copyValue(g), records: s.records}
	return p, nil
}
func (s *Simulator) authorize(sc r.SourceContext, p r.PreparedRead) (frozen, error) {
	return s.authorizeAt(s.now(), sc, p)
}

// authorizeAt is authorize at the given time, for a caller that reads the clock once.
func (s *Simulator) authorizeAt(now time.Time, sc r.SourceContext, p r.PreparedRead) (frozen, error) {
	g, e := s.catalog(sc)
	if e != nil {
		return frozen{}, e
	}
	f, ok := s.frozen[p.Ref]
	if !ok || r.Digest(f.prepared) != r.Digest(p) || sc != p.Context || !now.Before(p.ExpiresAt) {
		return frozen{}, r.ErrDenied
	}
	if g.Timezone != f.grant.Timezone || !reflect.DeepEqual(g.CurrencyPolicy, f.grant.CurrencyPolicy) {
		return frozen{}, r.ErrDenied
	}
	for _, c := range p.Companies {
		if !slices.Contains(g.Companies, c) {
			return frozen{}, r.ErrDenied
		}
	}
	// Covers the original authorized row universe, not an opaque policy hash.
	for _, row := range f.grant.Rows {
		if !slices.Contains(g.Rows, row) {
			return frozen{}, r.ErrDenied
		}
	}
	return f, nil
}
func (s *Simulator) AuthorizeCurrent(ctx context.Context, sc r.SourceContext, p r.PreparedRead) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e := s.authorize(sc, p)
	return e
}
func (s *Simulator) ReadPage(ctx context.Context, sc r.SourceContext, p r.PreparedRead, cursor string, limit int) (r.SourcePage, error) {
	if e := ctx.Err(); e != nil {
		return r.SourcePage{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, e := s.authorize(sc, p)
	if e != nil {
		return r.SourcePage{}, e
	}
	s.reads++
	return ReadFrozenPage(ctx, Fixture{Dataset: s.dataset, Records: f.records}, f.query, f.grant, p, cursor, limit)
}

func sum(values []string, scale int) (string, error) {
	total := new(big.Int)
	for _, v := range values {
		parts := strings.Split(v, ".")
		if scale > 0 && (len(parts) != 2 || len(parts[1]) != scale) {
			return "", r.ErrIntegrity
		}
		if scale == 0 && len(parts) != 1 {
			return "", r.ErrIntegrity
		}
		n, ok := new(big.Int).SetString(strings.ReplaceAll(v, ".", ""), 10)
		if !ok {
			return "", r.ErrIntegrity
		}
		total.Add(total, n)
	}
	negative := total.Sign() < 0
	digits := new(big.Int).Abs(total).String()
	if scale > 0 {
		for len(digits) <= scale {
			digits = "0" + digits
		}
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if negative {
		digits = "-" + digits
	}
	return digits, nil
}

var _ r.Adapter = (*Simulator)(nil)
