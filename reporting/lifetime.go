package reporting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Three lifetimes (ADR 0026, docs/S25A_SCOPE.md). Before it one time, the source read's expiry, bounded everything made from the read. Now:
//
//   - the read (PreparedRead.ExpiresAt, at most 15 minutes ahead): while it lasts the source serves pages, and a product can check a row
//     against its own frozen rows, which issuing needs;
//   - the scope (PreparedRead.ScopeExpiresAt, optional): until it passes the source answers AuthorizeCurrent for the read;
//   - the result (Job.KeepUntil, the expiry of the snapshot or the file the job makes): the read's expiry plus the time of the result's
//     class, never later than the scope.
//
// A source that declares no scope is served exactly as before: the scope is the read, nothing is kept beyond it, and no stored value
// changes its encoding.

// ScopeEnd is until when the source answers AuthorizeCurrent for the read: its ScopeExpiresAt, or its ExpiresAt when the source declared
// none.
func (p PreparedRead) ScopeEnd() time.Time {
	if p.ScopeExpiresAt.IsZero() {
		return p.ExpiresAt
	}
	return p.ScopeExpiresAt
}

// vouched says whether the source vouches for the read's scope beyond the read itself. Only then can anything made from the read outlive
// it; where it is false every path of the service is the one it was before ADR 0026.
func (p PreparedRead) vouched() bool { return p.ScopeEnd().After(p.ExpiresAt) }

// validLifetimes: the scope a read declares is absent, or not before the read's own expiry and within the years a product's time may name.
// The year is judged in the time's own zone as well: that is the year the time is encoded with, and one past 9999 cannot be encoded
// (the last hours of 9999 UTC are year 10000 east of Greenwich).
func (p PreparedRead) validLifetimes() bool {
	s := p.ScopeExpiresAt
	return s.IsZero() || (!s.Before(p.ExpiresAt) && inProductYears(s) && s.Year() <= 9999)
}

// Retention is how long a result is kept after the read it was made from has expired, by its class (ADR 0026 decision 3), always within the
// read's scope. A working snapshot (a person's freeze) has no time beyond its read. Zero is the default of each; a value outside its bounds
// is refused by Resolved, so by NewService.
type Retention struct {
	// Results is the time of every export file and of the snapshot of a scheduled run: default 7 days, 1 minute to 366 days.
	Results time.Duration
	// Issued is the time of an issued document's file: default 10 years, 1 minute to 100 years.
	Issued time.Duration
}

const (
	DefaultResultsRetention = 7 * 24 * time.Hour
	DefaultIssuedRetention  = 3650 * 24 * time.Hour
	MinRetention            = time.Minute
	MaxResultsRetention     = 366 * 24 * time.Hour
	MaxIssuedRetention      = 36500 * 24 * time.Hour
)

// Resolved replaces each zero with its default and refuses (ErrInvalid) a value outside its bounds.
func (r Retention) Resolved() (Retention, error) {
	one := func(v *time.Duration, def, max time.Duration) bool {
		if *v == 0 {
			*v = def
		}
		return *v >= MinRetention && *v <= max
	}
	if !one(&r.Results, DefaultResultsRetention, MaxResultsRetention) || !one(&r.Issued, DefaultIssuedRetention, MaxIssuedRetention) {
		return Retention{}, fmt.Errorf("a retention time is 0 (the default) or %s up to %s for results and %s for issued documents: %w", MinRetention, MaxResultsRetention, MaxIssuedRetention, ErrInvalid)
	}
	return r, nil
}

// KeepUntil is the expiry of a result of a class made from the read: the read's expiry plus the class's time, or the read's scope expiry
// when that is earlier. It is never before the read's expiry, and it is the read's expiry when the source vouches for no scope.
func KeepUntil(p PreparedRead, class time.Duration) time.Time {
	end := p.ExpiresAt.Add(class)
	if scope := p.ScopeEnd(); scope.Before(end) {
		end = scope
	}
	return end
}

// kept is what a job of the class freezes as its KeepUntil: the result's expiry when it is later than the read's, else the zero time, so
// that a job whose result lives as long as its read is exactly the job it was before ADR 0026.
func kept(p PreparedRead, class time.Duration) time.Time {
	if end := KeepUntil(p, class); end.After(p.ExpiresAt) {
		return end
	}
	return time.Time{}
}

// validKeep: a job's KeepUntil is absent, or later than its read's expiry and not later than the read's scope.
func validKeep(j Job) bool {
	return j.KeepUntil.IsZero() || (j.KeepUntil.After(j.Prepared.ExpiresAt) && !j.KeepUntil.After(j.Prepared.ScopeEnd()))
}

// ResultEnd is until when the result the job makes is kept and served: its KeepUntil, or its read's expiry when it froze none. Of a
// reduced job that froze none it is the zero time (its read is gone, and so is its result).
func (j Job) ResultEnd() time.Time {
	if j.KeepUntil.IsZero() {
		return j.Prepared.ExpiresAt
	}
	return j.KeepUntil
}

// PublishDeadline is the time after which the job may no longer publish. A snapshot job reads the source's pages and an issue job has the
// product check the row against its own frozen rows, so both need the read itself and end with it. An export needs its snapshot and the
// read's scope only (ADR 0026 decision 6): it may publish for as long as its result would be served. The stores fence publication by it.
func (j Job) PublishDeadline() time.Time {
	if j.Kind == "export" {
		return j.ResultEnd()
	}
	return j.Prepared.ExpiresAt
}

// ReduceAfter is the time after which, and the retention's grace, the payload of a finished job may be reduced (S04-B): the expiry of
// its result when it succeeded (ResultEnd: the read's own, or the later time the job keeps its result until), and the read's own expiry
// when it made nothing. A failed or cancelled job keeps no result, so its frozen query does not wait for the time a file would have been
// kept until; a job whose file is kept for years is not a candidate for reduction until that file's time has passed.
func (j Job) ReduceAfter() time.Time {
	if j.State == "succeeded" {
		return j.ResultEnd()
	}
	return j.Prepared.ExpiresAt
}

// carriesBasis: an artifact that outlives its read (its job froze a KeepUntil) carries the job's frozen query and read, by which it is
// authorized once its snapshot is gone (ADR 0026 decision 5); any other artifact carries none, and is what it always was.
func carriesBasis(j Job, a Artifact) bool {
	if j.KeepUntil.IsZero() {
		return a.Query == nil && a.Prepared == nil
	}
	return a.Query != nil && a.Prepared != nil && sameJSON(*a.Query, j.Query) && sameJSON(*a.Prepared, j.Prepared)
}

// sameJSON compares two values by their encoding, as the stores keep them. A value that cannot be encoded equals nothing: the caller
// refuses it, and a store is never made to panic by what it was asked to publish.
func sameJSON(a, b any) bool {
	x, ex := json.Marshal(a)
	y, ey := json.Marshal(b)
	return ex == nil && ey == nil && bytes.Equal(x, y)
}
