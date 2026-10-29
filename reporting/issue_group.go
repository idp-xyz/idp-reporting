package reporting

import (
	"fmt"
	"slices"
	"unicode/utf8"
)

// Issue by group (S03-K, docs/ADR_0004_TEMPLATE_SCHEMA_2.md decision 7). A schema 2.0 template makes one
// document per row group, and an issue job freezes the group in place of the row: its template fields, its
// key values, the snapshot's row indices and the digest of those rows. The platform never numbers a group
// document either: the source product authorizes it, numbers it and confirms the file (ADR 0003).

// IssueContractGroups is the version of the DocumentAuthority contract that carries a group (IssueIntent.Group).
// Version 1, which every adapter that implements DocumentAuthority speaks, carries one row.
const IssueContractGroups = 2

// MaxGroupFields is the most fields a group is made of (the template validator's limit).
const MaxGroupFields = 3

// maxGroupFieldBytes and maxGroupKeyBytes bound what a frozen group holds: a field id and a key value (a cell of
// the snapshot, which validCell holds to 4096 bytes).
const (
	maxGroupFieldBytes = 128
	maxGroupKeyBytes   = 4096
)

// IssueGroup is one row group of a frozen snapshot, as an issue job freezes it, the product is asked about it and
// the record keeps it. Fields are the template's group fields in order (none for a template without a group, whose
// group is the whole snapshot); Key holds the exact frozen strings of those fields, in that order; Rows are the
// snapshot's row indices of the group, strictly ascending and never none; RowsSHA256 is RowsDigest of those rows
// as the snapshot holds them.
type IssueGroup struct {
	Fields     []string
	Key        []string
	Rows       []int
	RowsSHA256 string
}

// VersionedAuthority is implemented by an adapter whose document authority speaks a version of the contract
// beyond 1: IssueContractVersion answers the highest version it speaks. An adapter without it speaks version 1.
// The platform asks only for what the adapter declares: a group intent goes to an adapter that returns
// IssueContractGroups, and to no other.
type VersionedAuthority interface {
	IssueContractVersion() int
}

// IssueContractVersion is the version of the DocumentAuthority contract the adapter declares: 1 unless it
// implements VersionedAuthority.
func IssueContractVersion(adapter any) int {
	if v, ok := adapter.(VersionedAuthority); ok {
		return v.IssueContractVersion()
	}
	return 1
}

// GroupIssueSupported reports whether the adapter can be sent a group intent: it implements DocumentAuthority
// and declares exactly contract version 2.
func GroupIssueSupported(adapter any) bool {
	_, ok := adapter.(DocumentAuthority)
	return ok && IssueContractVersion(adapter) == IssueContractGroups
}

// RowsDigest is the digest of a list of rows (IssueGroup.RowsSHA256): the SHA-256, in lowercase hexadecimal, of the
// rows as one JSON array of the rows' arrays of strings, with no white space and escaped as docs/API.md specifies
// for the digest of one row (Digest(row)), so that a product not written in Go can reproduce it. No rows is `[]`.
func RowsDigest(rows [][]string) string {
	if rows == nil {
		rows = [][]string{}
	}
	return Digest(rows)
}

// Clone copies the group with non-nil lists, so that a group of no fields is `[]` wherever it is encoded and
// compares and digests alike however it was made.
func (g IssueGroup) Clone() IssueGroup {
	return IssueGroup{Fields: append([]string{}, g.Fields...), Key: append([]string{}, g.Key...), Rows: append([]int{}, g.Rows...), RowsSHA256: g.RowsSHA256}
}

// Equal reports whether two groups are the same group; a nil group equals only a nil group, and an empty list
// equals a missing one.
func (g *IssueGroup) Equal(o *IssueGroup) bool {
	if g == nil || o == nil {
		return g == nil && o == nil
	}
	return slices.Equal(g.Fields, o.Fields) && slices.Equal(g.Key, o.Key) && slices.Equal(g.Rows, o.Rows) && g.RowsSHA256 == o.RowsSHA256
}

// cloned is a copy of the group a record keeps, nil for none: the record owns its lists.
func (g *IssueGroup) cloned() *IssueGroup {
	if g == nil {
		return nil
	}
	c := g.Clone()
	return &c
}

// View is how the views show the group.
func (g IssueGroup) View() *IssueGroupView {
	c := g.Clone()
	return &IssueGroupView{Fields: c.Fields, Key: c.Key, RowCount: len(c.Rows), RowsSHA256: c.RowsSHA256}
}

// ValidGroupShape: the group is well formed by itself, without a snapshot: at most MaxGroupFields distinct field
// ids, a key value for each, between 1 and MaxRows rows in strictly ascending order inside the snapshot's limit and
// a lowercase hexadecimal SHA-256. A group of no rows is not a document to issue.
func ValidGroupShape(g IssueGroup) bool {
	if len(g.Fields) > MaxGroupFields || len(g.Key) != len(g.Fields) || len(g.Rows) < 1 || len(g.Rows) > MaxRows || len(g.RowsSHA256) != 64 {
		return false
	}
	for i, f := range g.Fields {
		if f == "" || len(f) > maxGroupFieldBytes || !utf8.ValidString(f) || slices.Contains(g.Fields[:i], f) {
			return false
		}
	}
	for _, v := range g.Key {
		if len(v) > maxGroupKeyBytes || !utf8.ValidString(v) {
			return false
		}
	}
	prev := -1
	for _, i := range g.Rows {
		if i <= prev || i >= MaxRows {
			return false
		}
		prev = i
	}
	for i := 0; i < len(g.RowsSHA256); i++ {
		if c := g.RowsSHA256[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// GroupRows takes the rows of the group from the snapshot, in the group's order.
func GroupRows(snap Snapshot, rows []int) ([][]string, error) {
	out := make([][]string, 0, len(rows))
	for _, i := range rows {
		if i < 0 || i >= len(snap.Result.Rows) {
			return nil, ErrInvalid
		}
		out = append(out, snap.Result.Rows[i])
	}
	return out, nil
}

// NewIssueGroup makes the group of the snapshot rows with the given indices under the given template fields and
// key, digesting those rows as the snapshot holds them, and verifies it: VerifyGroup's refusals apply.
func NewIssueGroup(snap Snapshot, fields, key []string, rows []int) (IssueGroup, error) {
	picked, e := GroupRows(snap, rows)
	if e != nil {
		return IssueGroup{}, e
	}
	g := IssueGroup{Fields: fields, Key: key, Rows: rows, RowsSHA256: RowsDigest(picked)}.Clone()
	if e = VerifyGroup(snap, g); e != nil {
		return IssueGroup{}, e
	}
	return g, nil
}

// VerifyGroup checks a frozen group against the snapshot it was made from, as admission and the worker do before
// the product is asked: the group is well formed, every field is a dimension of the snapshot, every row of the
// group exists and has the group's key, the group is whole (no other row of the snapshot has that key; without
// fields it is every row) and the digest is the one of those rows as the snapshot holds them. It refuses with
// ErrInvalid for a group that is not this snapshot's, ErrIntegrity for a snapshot whose rows do not have the
// shape of its columns; the errors carry no value.
func VerifyGroup(snap Snapshot, g IssueGroup) error {
	if !ValidGroupShape(g) {
		return fmt.Errorf("the group is not well formed: %w", ErrInvalid)
	}
	columns := map[string]int{}
	for i, c := range snap.Result.Columns {
		columns[c.ID] = i
	}
	at := make([]int, len(g.Fields))
	for k, f := range g.Fields {
		i, ok := columns[f]
		if !ok || !slices.Contains(snap.Query.Dimensions, f) {
			return fmt.Errorf("a group field is not a dimension of the snapshot: %w", ErrInvalid)
		}
		at[k] = i
	}
	rows := snap.Result.Rows
	matches := func(row []string) (bool, error) {
		if len(row) != len(snap.Result.Columns) {
			return false, fmt.Errorf("a row of the snapshot does not fit its columns: %w", ErrIntegrity)
		}
		for k, i := range at {
			if row[i] != g.Key[k] {
				return false, nil
			}
		}
		return true, nil
	}
	count := 0
	for _, row := range rows {
		ok, e := matches(row)
		if e != nil {
			return e
		}
		if ok {
			count++
		}
	}
	if count != len(g.Rows) {
		return fmt.Errorf("the group is not the rows of the snapshot with its key: %w", ErrInvalid)
	}
	picked := make([][]string, 0, len(g.Rows))
	for _, i := range g.Rows {
		if i >= len(rows) {
			return fmt.Errorf("a row of the group is not in the snapshot: %w", ErrInvalid)
		}
		if ok, e := matches(rows[i]); e != nil || !ok {
			if e == nil {
				e = fmt.Errorf("a row of the group does not have its key: %w", ErrInvalid)
			}
			return e
		}
		picked = append(picked, rows[i])
	}
	if RowsDigest(picked) != g.RowsSHA256 {
		return fmt.Errorf("the group's rows are not the digested rows: %w", ErrInvalid)
	}
	return nil
}

// IssueGroupRequestHash binds a group issue job to everything it freezes: the template, the snapshot and the
// resolved group, so that a request that names the group by its key and one that names it by one of its rows are
// the same request. Its form differs from IssueRequestHash's, so that a row request and a group request never
// share a hash.
func IssueGroupRequestHash(templateID string, version int, templateSHA256, snapshotID string, g IssueGroup) string {
	return Digest(struct {
		TemplateID      string
		TemplateVersion int
		TemplateSHA256  string
		SnapshotID      string
		Group           IssueGroup
	}{templateID, version, templateSHA256, snapshotID, g.Clone()})
}
