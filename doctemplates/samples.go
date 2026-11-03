package doctemplates

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	r "idp.local/reporting/reporting"
)

// The sample pack (S06-B): read-only templates the platform ships, embedded from samples/, that a person previews with the sample's own
// rows and copies into their own store, mapping the sample's field slots to the columns of a dataset. A sample binds to no dataset: its
// document names the placeholder binding SampleBinding, and its text, code and table elements name field slots (ids of the sample's
// own fields) in place of dataset columns, until a copy replaces each slot (Bind).
//
// A sample is one JSON file, samples/<kind>.json:
//
//	{"kind", "name", "domain", "flow", "description", "fields": [{"id","label","type","scope"[,"metric","scale"]}],
//	 "rows": [{<field id>: <text>, ...}], "document": <a 2.x document bound to SampleBinding>}
//
// Files named <kind>.record.json are the converter's records of what it dropped, not samples; the directory prototype/ is the
// converter's input. Neither is embedded.

//go:embed samples/*.json
var sampleFiles embed.FS

// SampleBinding is the binding a sample's document names, and the snapshot of its preview is of.
var SampleBinding = Binding{Integration: "sample", Dataset: r.Ref{ID: "sample", Version: "1"}}

// Sample limits.
const (
	MaxSampleFields = 128
	MaxSampleRows   = 50
	MaxSampleText   = 200  // name, flow and domain
	MaxSampleDesc   = 1000 // description
	MaxSampleLabel  = MaxLabelRunes
	// SampleFieldScopes
	ScopeDocument = "document"
	ScopeRow      = "row"
)

var sampleTypes = map[string]bool{"text": true, "integer": true, "decimal": true, "date": true}

// SampleField is a field slot of a sample: what a copy maps to a dataset column or a literal.
type SampleField struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"`  // text, integer, decimal, date
	Scope string `json:"scope"` // document: the same in every row of a document; row: one per row
	// Metric marks a row field as a measure the table sums (the preview's snapshot treats it as a metric); it needs a number type.
	Metric bool `json:"metric,omitempty"`
	Scale  int  `json:"scale,omitempty"` // decimal places of a decimal field
}

// Sample is one template of the pack.
type Sample struct {
	Kind        string              `json:"kind"`
	Name        string              `json:"name"`
	Domain      string              `json:"domain"`
	Flow        string              `json:"flow"`
	Description string              `json:"description"`
	Fields      []SampleField       `json:"fields"`
	Rows        []map[string]string `json:"rows"`
	Document    Document            `json:"document"`
}

// Preset is the page preset of the sample's document.
func (s Sample) Preset() string {
	p, _ := s.Document.PagePreset()
	return p.ID
}

// ElementCount is the number of elements of the sample's document, the table included.
func (s Sample) ElementCount() int { return len(s.Document.Items()) }

func (s Sample) field(id string) (SampleField, bool) {
	for _, f := range s.Fields {
		if f.ID == id {
			return f, true
		}
	}
	return SampleField{}, false
}

// validKind: a lower-case identifier of 1 to 64 characters; a leading underscore marks a fixture of the tests.
func validKind(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for i, c := range k {
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '_' && c != '-' || (i == 0 && c == '-') {
			return false
		}
	}
	return true
}

func oneLine(s string, limit int) bool {
	return s != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= limit && strings.TrimSpace(s) == s && !hasControls(s)
}

// refs lists the field slots an element names.
func itemFieldRefs(it Item) []string {
	switch it.Type {
	case TypeText, TypeBarcode, TypeQRCode:
		if it.Source == SourceField || it.Source == SourceGroupSum {
			return []string{it.Field}
		}
	case TypeTable:
		out := make([]string, len(it.Columns))
		for i, c := range it.Columns {
			out[i] = c.Field
		}
		return out
	}
	return nil
}

// validateSample checks a sample as a whole: its text, its field slots, its rows, and its document, which must be valid, bound to
// SampleBinding, without a background, and must name only declared slots, its group fields document-scope ones.
func validateSample(s Sample) error {
	if !validKind(s.Kind) || !oneLine(s.Name, MaxNameRunes) || !oneLine(s.Domain, MaxSampleText) || !oneLine(s.Flow, MaxSampleText) ||
		s.Description == "" || !utf8.ValidString(s.Description) || utf8.RuneCountInString(s.Description) > MaxSampleDesc || hasControls(s.Description) ||
		len(s.Fields) < 1 || len(s.Fields) > MaxSampleFields || len(s.Rows) < 1 || len(s.Rows) > MaxSampleRows {
		return r.ErrInvalid
	}
	seen := map[string]bool{}
	for _, f := range s.Fields {
		if f.ID == "" || len(f.ID) > 128 || !utf8.ValidString(f.ID) || seen[f.ID] || !oneLine(f.Label, MaxSampleLabel) || !sampleTypes[f.Type] || (f.Scope != ScopeDocument && f.Scope != ScopeRow) ||
			(f.Metric && ((f.Type != "integer" && f.Type != "decimal") || f.Scope != ScopeRow)) || f.Scale < 0 || f.Scale > 6 || (f.Scale > 0 && f.Type != "decimal") {
			return r.ErrInvalid
		}
		seen[f.ID] = true
	}
	for _, row := range s.Rows {
		if len(row) != len(s.Fields) {
			return r.ErrInvalid
		}
		for _, f := range s.Fields {
			v, ok := row[f.ID]
			if !ok || !utf8.ValidString(v) || hasControls(v) || len([]rune(v)) > MaxTextRunes || ((f.Type == "integer" || f.Type == "decimal") && !exactNumber.MatchString(v)) {
				return r.ErrInvalid
			}
		}
	}
	// A document-scope field has one value in all rows.
	for _, f := range s.Fields {
		if f.Scope == ScopeDocument {
			for _, row := range s.Rows[1:] {
				if row[f.ID] != s.Rows[0][f.ID] {
					return r.ErrInvalid
				}
			}
		}
	}
	d := s.Document
	if !d.Banded() || d.Binding != SampleBinding || d.Name != "" || d.Page != nil && d.Page.Print != nil && d.Page.Print.Background != nil || Validate(d) != nil {
		return r.ErrInvalid
	}
	if d.Group != nil {
		for _, g := range d.Group.Fields {
			if f, ok := s.field(g); !ok || f.Scope != ScopeDocument || f.Metric {
				return r.ErrInvalid
			}
		}
	}
	for _, bi := range d.Items() {
		for _, ref := range itemFieldRefs(bi.Item) {
			f, ok := s.field(ref)
			if !ok || (bi.Item.Source == SourceGroupSum && !f.Metric) {
				return r.ErrInvalid
			}
		}
		if bi.Item.Type == TypeImage {
			return r.ErrInvalid // a sample draws no private image; an image slot is a rectangle with a label
		}
	}
	return nil
}

// decodeSample reads one sample file strictly.
func decodeSample(data []byte) (Sample, error) {
	var s Sample
	if e := r.DecodeStrict(bytes.NewReader(data), &s); e != nil {
		return Sample{}, e
	}
	return s, validateSample(s)
}

// DecodeSample reads and validates one sample's JSON.
func DecodeSample(data []byte) (Sample, error) { return decodeSample(data) }

var (
	samplesOnce sync.Once
	samples     []Sample
	samplesErr  error
)

// Samples returns the pack in the order of its kinds. A file that is not a valid sample, or whose kind is not its name, is an error
// of the build, not of a request: the first call reports it and so does every later one.
func Samples() ([]Sample, error) {
	samplesOnce.Do(func() { samples, samplesErr = loadSamples(sampleFiles, "samples") })
	return samples, samplesErr
}

func loadSamples(files fs.FS, dir string) ([]Sample, error) {
	entries, e := fs.ReadDir(files, dir)
	if e != nil {
		return nil, e
	}
	var out []Sample
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".record.json") {
			continue
		}
		data, e := fs.ReadFile(files, dir+"/"+name)
		if e != nil {
			return nil, e
		}
		s, e := decodeSample(data)
		if e != nil || s.Kind != strings.TrimSuffix(name, ".json") {
			return nil, fmt.Errorf("doctemplates: sample %s is not valid", name)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out, nil
}

// SampleByKind finds a sample of the pack.
func SampleByKind(kind string) (Sample, bool, error) {
	all, e := Samples()
	if e != nil {
		return Sample{}, false, e
	}
	for _, s := range all {
		if s.Kind == kind {
			return s, true, nil
		}
	}
	return Sample{}, false, nil
}

// Snapshot is the frozen snapshot of the sample's own rows, with the sample's columns, for the preview: document-scope fields are
// its dimensions, metric fields its metrics. It is of SampleBinding, so only the sample's own document binds it.
func (s Sample) Snapshot() r.Snapshot {
	snap := r.Snapshot{ID: "sample-" + s.Kind, Owner: r.Owner{Integration: SampleBinding.Integration}}
	snap.Query = r.QuerySpec{Integration: SampleBinding.Integration, Dataset: SampleBinding.Dataset}
	for _, f := range s.Fields {
		c := r.Column{ID: f.ID, Label: f.Label, Type: "string", Scale: f.Scale}
		switch f.Type {
		case "integer", "decimal":
			c.Type = f.Type
		case "date":
			c.Type = "date"
		}
		snap.Result.Columns = append(snap.Result.Columns, c)
		if f.Metric {
			snap.Query.Metrics = append(snap.Query.Metrics, r.Ref{ID: f.ID, Version: "1"})
		} else {
			snap.Query.Dimensions = append(snap.Query.Dimensions, f.ID)
		}
	}
	for _, row := range s.Rows {
		out := make([]string, len(s.Fields))
		for i, f := range s.Fields {
			out[i] = row[f.ID]
		}
		snap.Result.Rows = append(snap.Result.Rows, out)
	}
	snap.Result.Totals = map[string]string{}
	return snap
}

// BindPreview binds the sample's document to the first document of its own rows, as a preview of a template is bound: the values by
// element id and the table.
func (s Sample) BindPreview() (map[string]string, *TableData, error) {
	snap := s.Snapshot()
	group, e := GroupOf(s.Document, snap, 0)
	if e != nil {
		return nil, nil, e
	}
	return BindGroup(s.Document, snap, group)
}

// FieldMapping says what a sample field slot becomes in a copy: a column of the dataset, or a literal text. Exactly one is set.
type FieldMapping struct {
	Column  *string `json:"column"`
	Literal *string `json:"literal"`
}

// UnmarshalJSON reads {"column": id} or {"literal": text}, and nothing else.
func (m *FieldMapping) UnmarshalJSON(data []byte) error {
	type wire FieldMapping
	var value wire
	if e := r.DecodeStrict(bytes.NewReader(data), &value); e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(data, &fields); e != nil || len(fields) != 1 || (value.Column == nil) == (value.Literal == nil) {
		return r.ErrInvalid
	}
	*m = FieldMapping(value)
	return nil
}

// Bind makes the template a copy of the sample is: the sample's document bound to the dataset binding, with every field slot replaced by
// what the mapping says (an unmapped slot by the literal "[<label>]"), and the name. The result is a candidate for the save path, which
// validates it as it validates every template; Bind itself refuses only a mapping that is no mapping of this sample (a slot the sample
// lacks) and a document that is left without a table column. A table column whose slot is a literal is dropped and its width shared
// among the others (a literal has no column to print); a group field whose slot is a literal is dropped from the group.
func (s Sample) Bind(binding Binding, name string, mapping map[string]FieldMapping) (Document, error) {
	if !validBinding(binding) || !validName(name) {
		return Document{}, r.ErrInvalid
	}
	for slot := range mapping {
		if _, ok := s.field(slot); !ok {
			return Document{}, r.ErrInvalid
		}
	}
	// A deep copy through the document's own form.
	raw, e := json.Marshal(s.Document)
	if e != nil {
		return Document{}, e
	}
	var d Document
	if e = json.Unmarshal(raw, &d); e != nil {
		return Document{}, e
	}
	d.Binding, d.Name = binding, name
	// resolve says what a slot is in the copy: the column it is mapped to, or the literal text.
	resolve := func(slot string) (column, literal string, isColumn bool) {
		if m, ok := mapping[slot]; ok {
			if m.Column != nil {
				return *m.Column, "", true
			}
			return "", *m.Literal, false
		}
		f, _ := s.field(slot)
		return "", "[" + f.Label + "]", false
	}
	if d.Group != nil {
		var fields []string
		seen := map[string]bool{}
		for _, g := range d.Group.Fields {
			if c, _, ok := resolve(g); ok && !seen[c] {
				fields = append(fields, c)
				seen[c] = true
			}
		}
		if len(fields) == 0 {
			d.Group = nil
		} else {
			d.Group = &GroupSpec{Fields: fields}
		}
	}
	bands := []*Band{d.Bands.PageHeader, d.Bands.DocumentHeader, d.Bands.DocumentFooter, d.Bands.PageFooter}
	for _, band := range bands {
		if band == nil {
			continue
		}
		for i, it := range band.Elements {
			if it.Source != SourceField && it.Source != SourceGroupSum {
				continue
			}
			if it.Type != TypeText && it.Type != TypeBarcode && it.Type != TypeQRCode {
				continue
			}
			if column, literal, ok := resolve(it.Field); ok {
				band.Elements[i].Field = column
			} else {
				band.Elements[i].Source, band.Elements[i].Field, band.Elements[i].Text = SourceLiteral, "", literal
			}
		}
	}
	table := &d.Bands.Detail.Elements[0]
	var kept []ItemColumn
	dropped := 0
	for _, c := range table.Columns {
		if column, _, ok := resolve(c.Field); ok {
			c.Field = column
			kept = append(kept, c)
		} else {
			dropped += tenths(c.Width)
		}
	}
	if len(kept) == 0 {
		return Document{}, r.ErrInvalid
	}
	// The width of the dropped columns goes to the others in proportion to theirs, on the 0.1 mm grid, the rest to the last.
	if dropped > 0 {
		have := 0
		for _, c := range kept {
			have += tenths(c.Width)
		}
		left := dropped
		for i := range kept {
			add := dropped * tenths(kept[i].Width) / have
			if i == len(kept)-1 {
				add = left
			}
			kept[i].Width = float64(tenths(kept[i].Width)+add) / 10
			left -= add
		}
	}
	table.Columns = kept
	return d, nil
}
