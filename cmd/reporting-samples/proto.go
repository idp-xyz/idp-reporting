package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The input of the converter: one file of doctemplates/samples/prototype per template, as scripts/prototype_templates.mjs wrote it
// from the prototype. The structs name the properties the converter reads; every other property of a node is found by keys() and
// goes into the record as one that was not mapped, so that nothing is dropped silently.

type protoStyle struct {
	Font        string  `json:"font"`
	Size        float64 `json:"size"`
	Color       string  `json:"color"`
	Bold        bool    `json:"bold"`
	Italic      bool    `json:"italic"`
	Align       string  `json:"align"`
	LineHeight  float64 `json:"lineHeight"`
	LetterSpace float64 `json:"letterSpace"`
}

type protoColumn struct {
	Field  string  `json:"field"`
	Label  string  `json:"label"`
	Width  float64 `json:"width"`
	Align  string  `json:"align"`
	Format string  `json:"format"`
}

type protoNode struct {
	ID     string     `json:"id"`
	Type   string     `json:"type"`
	Band   string     `json:"band"`
	Name   string     `json:"name"`
	X      float64    `json:"x"`
	Y      float64    `json:"y"`
	W      float64    `json:"w"`
	H      float64    `json:"h"`
	Hidden bool       `json:"hidden"`
	Locked bool       `json:"locked"`
	Group  string     `json:"group"`
	Style  protoStyle `json:"style"`
	// text, signature, pageNumber, watermark
	Text    string `json:"text"`
	Pattern string `json:"pattern"`
	// field, barcode, qr, aggregate
	Field    string `json:"field"`
	Scope    string `json:"scope"`
	Label    string `json:"label"`
	Format   string `json:"format"`
	Method   string `json:"method"`
	CodeType string `json:"codeType"`
	ShowText bool   `json:"showText"`
	// image
	Image string `json:"image"`
	// rect
	Fill string `json:"fill"`
	// table
	Columns       []protoColumn `json:"columns"`
	RowsPerPage   float64       `json:"rowsPerPage"`
	MinRowHeight  float64       `json:"minRowHeight"`
	Padding       float64       `json:"padding"`
	Wrap          bool          `json:"wrap"`
	RepeatHeader  bool          `json:"repeatHeader"`
	Striped       bool          `json:"striped"`
	GroupField    string        `json:"groupField"`
	GroupTotals   bool          `json:"groupTotals"`
	Carry         bool          `json:"carry"`
	Subtotal      bool          `json:"subtotal"`
	SortField     string        `json:"sortField"`
	SortDirection string        `json:"sortDirection"`
	NegativeRed   bool          `json:"negativeRed"`
	HeaderColor   string        `json:"headerColor"`

	extra []string // properties the converter does not read, sorted
}

type protoPage struct {
	Preset      string  `json:"preset"`
	Width       float64 `json:"width"`
	Height      float64 `json:"height"`
	Orientation string  `json:"orientation"`
	Margins     struct {
		Top, Bottom, Left, Right float64
	} `json:"margins"`
	Mode       string  `json:"mode"`
	OffsetX    float64 `json:"offsetX"`
	OffsetY    float64 `json:"offsetY"`
	Background string  `json:"background"`
	Opacity    float64 `json:"opacity"`
	FormGuide  string  `json:"formGuide"`
}

type protoBands struct {
	PageHeader   float64 `json:"pageHeader"`
	ReportHeader float64 `json:"reportHeader"`
	ReportFooter float64 `json:"reportFooter"`
	PageFooter   float64 `json:"pageFooter"`
}

type protoParam struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type protoDoc struct {
	Schema     string       `json:"schema"`
	Name       string       `json:"name"`
	Kind       string       `json:"kind"`
	DatasetID  string       `json:"datasetId"`
	Page       protoPage    `json:"page"`
	Bands      protoBands   `json:"bands"`
	Parameters []protoParam `json:"parameters"`
	Nodes      []protoNode  `json:"nodes"`
}

type protoField struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"`
	Scale int    `json:"scale"`
}

type protoDataset struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Fields      []protoField `json:"fields"`
}

type protoFile struct {
	Catalog struct {
		Domain      string `json:"domain"`
		Flow        string `json:"flow"`
		DomainLabel string `json:"domain_label"`
		FlowLabel   string `json:"flow_label"`
	} `json:"catalog"`
	Source struct {
		TemplateName string `json:"template_name"`
		Kind         string `json:"kind"`
		File         string `json:"file"`
		Versions     int    `json:"versions"`
		Published    int    `json:"published"`
	} `json:"source"`
	Doc     protoDoc          `json:"doc"`
	Dataset protoDataset      `json:"dataset"`
	Rows    []map[string]any  `json:"rows"`
	Meta    map[string]string `json:"meta"`
}

// knownKeys are the properties of a node that the converter reads or deliberately ignores (the bookkeeping of the editor: name,
// locked, group). A key outside the list for the node's type is a property the converter does not know: it is recorded.
var commonKeys = []string{"id", "type", "band", "name", "x", "y", "w", "h", "hidden", "locked", "group", "style"}
var typeKeys = map[string][]string{
	"text":       {"text"},
	"field":      {"field", "scope", "label", "format"},
	"table":      {"columns", "rowsPerPage", "minRowHeight", "padding", "wrap", "repeatHeader", "striped", "groupField", "groupTotals", "carry", "subtotal", "sortField", "sortDirection", "negativeRed", "headerColor"},
	"line":       {},
	"rect":       {"fill"},
	"image":      {"image"},
	"signature":  {"text"},
	"summary":    {},
	"aggregate":  {"field", "scope", "method", "label", "format"},
	"barcode":    {"field", "scope", "text", "codeType", "codeSettings", "showText"},
	"qr":         {"field", "scope", "text", "codeType", "codeSettings", "showText"},
	"pageNumber": {"text", "pattern"},
	"watermark":  {"text"},
}

// The node keys that identify a field of one of the prototype's fixed forms (the form's own field map); they restate field.
var formMapKeys = []string{"mawbField", "bolField", "parcelField"}

func nodeExtra(raw map[string]json.RawMessage, kind string) []string {
	known := map[string]bool{}
	for _, k := range commonKeys {
		known[k] = true
	}
	for _, k := range typeKeys[kind] {
		known[k] = true
	}
	for _, k := range formMapKeys {
		known[k] = true
	}
	var out []string
	for k := range raw {
		if !known[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// decodeProto reads one extraction file strictly enough: an unknown key at the top is refused; a node's unknown keys are kept.
func decodeProto(data []byte) (protoFile, error) {
	var pf protoFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return pf, err
	}
	for k := range top {
		switch k {
		case "catalog", "source", "doc", "dataset", "rows", "meta":
		default:
			return pf, fmt.Errorf("unknown key %q in the extraction file", k)
		}
	}
	if err := dec.Decode(&pf); err != nil {
		return pf, err
	}
	var rawDoc struct {
		Doc struct {
			Nodes []map[string]json.RawMessage `json:"nodes"`
		} `json:"doc"`
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(data, &rawDoc); err != nil {
		return pf, err
	}
	if len(rawDoc.Doc.Nodes) != len(pf.Doc.Nodes) {
		return pf, fmt.Errorf("nodes")
	}
	for i := range pf.Doc.Nodes {
		pf.Doc.Nodes[i].extra = nodeExtra(rawDoc.Doc.Nodes[i], pf.Doc.Nodes[i].Type)
	}
	return pf, nil
}

// rowValue turns a value of a prototype row into the text a sample holds: strings as they are, numbers by their digits.
func rowValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", x), "0"), ".")
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	return fmt.Sprint(v)
}

// loadDir reads every extraction file of a directory (not inventory.json), in the order of their names.
func loadDir(dir string) ([]protoFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []protoFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || name == "inventory.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		pf, err := decodeProto(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if pf.Source.File != strings.TrimSuffix(name, ".json") {
			return nil, fmt.Errorf("%s: the file names itself %q", name, pf.Source.File)
		}
		out = append(out, pf)
	}
	return out, nil
}
