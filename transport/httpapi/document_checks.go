package httpapi

import (
	"context"
	"errors"
	"idp.local/reporting/doctemplates"
	r "idp.local/reporting/reporting"
	"net/http"
	"sort"
	"unicode/utf8"
)

// DocumentChecker is the layout check of one row that the issue worker runs before it asks a
// product for a number (the PDF writer's CheckIssue: the fonts' plain advances, no Typst process,
// no slot). A renderer that also implements it gets POST /document-checks with the previews.
type DocumentChecker interface {
	CheckIssue(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData) error
}

// ResolvedChecker is the check of a schema 2.1 template (ADR 0005): the PDF writer's CheckIssueWith, with the bytes of the template's
// images, which proves the layout for every number the source product may assign. A checker without it cannot check a 2.1 template, which is then unsupported.
type ResolvedChecker interface {
	CheckIssueWith(context.Context, doctemplates.Document, map[string]string, []byte, *doctemplates.TableData, doctemplates.Resolved) error
}

// MaxDocumentCheckRows is the most rows one check answers: the first and the last row and, for
// each of the 24 boxes, the longest and the shortest value.
const MaxDocumentCheckRows = 50

// checkRow is one answered row: never a value, only its index, whether it passed, the code a
// preview of it would answer and, for a boundary sample, why it was picked.
type checkRow struct {
	RowIndex int                  `json:"row_index"`
	OK       bool                 `json:"ok"`
	Code     string               `json:"code,omitempty"`
	Detail   *doctemplates.Detail `json:"detail,omitempty"` // why and where a refused row is refused, as a preview's answer says it
	Reasons  []string             `json:"reasons,omitempty"`
}

// mountDocumentChecks mounts POST /document-checks: the current template against rows of a frozen
// snapshot, checked without rendering. Authorization is the preview's: the snapshot is the
// caller's, the document's fields and background are authorized, and both are checked again
// before the answer.
func mountDocumentChecks(mux *http.ServeMux, s *r.Service, auth Authenticate, checker DocumentChecker, assets *doctemplates.Assets) {
	mux.HandleFunc("POST /api/reporting/v1/document-checks", authenticated(auth, func(w http.ResponseWriter, req *http.Request, p r.Principal) error {
		var input struct {
			Config     doctemplates.Document `json:"config"`
			SnapshotID string                `json:"snapshot_id"`
			Rows       *[]int                `json:"rows"`
			Samples    *string               `json:"samples"`
		}
		if e := decode(req, &input); e != nil {
			return e
		}
		// Either explicit rows or the boundary samples, never both and never neither.
		if (input.Rows == nil) == (input.Samples == nil) {
			return r.ErrInvalid
		}
		var rows []int
		if input.Samples != nil {
			if *input.Samples != "boundary" {
				return r.ErrInvalid
			}
		} else {
			if rows = append([]int(nil), (*input.Rows)...); len(rows) < 1 || len(rows) > MaxDocumentCheckRows {
				return r.ErrInvalid
			}
			sort.Ints(rows)
			for i, v := range rows {
				if v < 0 || (i > 0 && rows[i-1] == v) {
					return r.ErrInvalid
				}
			}
		}
		if e := doctemplates.Validate(input.Config); e != nil {
			return e
		}
		return checkDocumentRows(w, req, s, p, checker, input.Config, input.SnapshotID, rows, input.Samples != nil, assets)
	}))
}

// checkRowPick is a row to check, with the reasons a boundary sample picked it.
type checkRowPick struct {
	index   int
	reasons []string
}

func checkDocumentRows(w http.ResponseWriter, req *http.Request, s *r.Service, p r.Principal, checker DocumentChecker, config doctemplates.Document, snapshotID string, explicit []int, boundary bool, assets *doctemplates.Assets) error {
	ctx := req.Context()
	snap, e := s.AuthorizedSnapshot(ctx, p, snapshotID)
	if e != nil {
		return e
	}
	if config.Binding.Integration != snap.Owner.Integration || config.Binding.Dataset != snap.Query.Dataset {
		return r.ErrDenied
	}
	if config.Banded() {
		return checkGroupRows(w, req, s, p, checker, config, snap, snapshotID, explicit, boundary, assets)
	}
	var picks []checkRowPick
	if boundary {
		if picks, e = boundaryRows(config, snap); e != nil {
			return e
		}
	} else {
		for _, v := range explicit {
			if v >= len(snap.Result.Rows) {
				return r.ErrInvalid
			}
			picks = append(picks, checkRowPick{index: v})
		}
	}
	table, e := doctemplates.BindTable(config, snap)
	if e != nil {
		return e
	}
	// Bind each row as a preview does. A value the preview cannot print is that row's refusal;
	// any other refusal (a field the snapshot lacks, a short row) is the whole request's.
	values := make([]map[string]string, len(picks))
	refused := make([]error, len(picks))
	for i, v := range picks {
		bound, e := doctemplates.Bind(config, snap, v.index)
		if errors.Is(e, r.ErrUnsupported) {
			refused[i] = e
		} else if e != nil {
			return e
		}
		values[i] = bound
	}
	if e = authorizeDocument(req, s, p, config, assets); e != nil {
		return e
	}
	background, e := resolveDocumentBackground(p, config, assets)
	if e != nil {
		return e
	}
	if config.Print != nil && config.Print.Mode == "variables" {
		background = nil
	}
	answer := make([]checkRow, 0, len(picks))
	for i, v := range picks {
		if e = ctx.Err(); e != nil {
			return e
		}
		e = refused[i]
		if e == nil {
			e = checker.CheckIssue(ctx, config, values[i], background, table)
		}
		row := checkRow{RowIndex: v.index, OK: e == nil, Reasons: v.reasons}
		if e != nil {
			// A layout or binding refusal of this row only; anything else, a context error
			// included, is the whole request's.
			if !errors.Is(e, r.ErrUnsupported) || ctx.Err() != nil {
				return e
			}
			row.Code, _, _ = classify(e)
			if detail, found := doctemplates.DetailOf(e); found {
				row.Detail = &detail
			}
		}
		answer = append(answer, row)
	}
	if _, e = s.AuthorizedSnapshot(ctx, p, snapshotID); e != nil {
		return e
	}
	if e = authorizeDocument(req, s, p, config, assets); e != nil {
		return e
	}
	w.Header().Set("Cache-Control", "no-store")
	return send(w, 200, map[string]any{"rows": answer})
}

// boundaryRows picks the rows a template is most likely to refuse, in row order: the first and the
// last row and, for each row-field box, the rows with the longest and the shortest value by display
// width, ties to the lowest index. A box's field the snapshot lacks is a denial and a short row an
// integrity failure, as when binding. At most 2 + 2 * 24 = MaxDocumentCheckRows rows.
func boundaryRows(config doctemplates.Document, snap r.Snapshot) ([]checkRowPick, error) {
	count := len(snap.Result.Rows)
	if count == 0 {
		return nil, nil
	}
	why := map[int][]string{}
	add := func(index int, reason string) { why[index] = append(why[index], reason) }
	add(0, "first")
	add(count-1, "last")
	columns := map[string]int{}
	for i, c := range snap.Result.Columns {
		columns[c.ID] = i
	}
	for _, e := range config.Elements {
		if e.Source != "row" {
			continue
		}
		column, ok := columns[e.Field]
		if !ok {
			return nil, r.ErrDenied
		}
		longest, shortest, most, least := 0, 0, -1, int(^uint(0)>>1)
		for i, row := range snap.Result.Rows {
			if column >= len(row) {
				return nil, r.ErrIntegrity
			}
			// Strict comparisons keep the lowest index on a tie.
			if w := displayWidth(row[column]); w > most {
				longest, most = i, w
			}
			if w := displayWidth(row[column]); w < least {
				shortest, least = i, w
			}
		}
		add(longest, "longest:"+e.ID)
		add(shortest, "shortest:"+e.ID)
	}
	picks := make([]checkRowPick, 0, len(why))
	for index, reasons := range why {
		picks = append(picks, checkRowPick{index: index, reasons: reasons})
	}
	sort.Slice(picks, func(i, j int) bool { return picks[i].index < picks[j].index })
	if len(picks) > MaxDocumentCheckRows {
		picks = picks[:MaxDocumentCheckRows]
	}
	return picks, nil
}

// displayWidth is the width of a value in columns: an East Asian wide or fullwidth character
// counts two, every other character one.
func displayWidth(s string) int {
	width := 0
	for len(s) > 0 {
		c, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		width++
		if wide(c) {
			width++
		}
	}
	return width
}

// wideRanges are the code points whose East Asian Width is W or F (Unicode 15.1), as pairs of
// first and last, in order.
var wideRanges = [][2]rune{
	{0x1100, 0x115F}, {0x231A, 0x231B}, {0x2329, 0x232A}, {0x23E9, 0x23EC}, {0x23F0, 0x23F0}, {0x23F3, 0x23F3},
	{0x25FD, 0x25FE}, {0x2614, 0x2615}, {0x2648, 0x2653}, {0x267F, 0x267F}, {0x2693, 0x2693}, {0x26A1, 0x26A1}, {0x26AA, 0x26AB}, {0x26BD, 0x26BE}, {0x26C4, 0x26C5}, {0x26CE, 0x26CE},
	{0x26D4, 0x26D4}, {0x26EA, 0x26EA}, {0x26F2, 0x26F3}, {0x26F5, 0x26F5}, {0x26FA, 0x26FA}, {0x26FD, 0x26FD},
	{0x2705, 0x2705}, {0x270A, 0x270B}, {0x2728, 0x2728}, {0x274C, 0x274C}, {0x274E, 0x274E}, {0x2753, 0x2755},
	{0x2757, 0x2757}, {0x2795, 0x2797}, {0x27B0, 0x27B0}, {0x27BF, 0x27BF}, {0x2B1B, 0x2B1C}, {0x2B50, 0x2B50},
	{0x2B55, 0x2B55}, {0x2E80, 0x2E99}, {0x2E9B, 0x2EF3}, {0x2F00, 0x2FD5}, {0x2FF0, 0x303E}, {0x3041, 0x3096},
	{0x3099, 0x30FF}, {0x3105, 0x312F}, {0x3131, 0x318E}, {0x3190, 0x31E5}, {0x31EF, 0x321E}, {0x3220, 0x3247},
	{0x3250, 0x4DBF}, {0x4E00, 0xA48C}, {0xA490, 0xA4C6}, {0xA960, 0xA97C}, {0xAC00, 0xD7A3}, {0xF900, 0xFAFF}, {0xFE10, 0xFE19},
	{0xFE30, 0xFE52}, {0xFE54, 0xFE66}, {0xFE68, 0xFE6B}, {0xFF01, 0xFF60}, {0xFFE0, 0xFFE6}, {0x16FE0, 0x16FE4},
	{0x16FF0, 0x16FF1}, {0x17000, 0x187F7}, {0x18800, 0x18CD5}, {0x18D00, 0x18D08}, {0x1AFF0, 0x1AFFE},
	{0x1B000, 0x1B122}, {0x1B132, 0x1B132}, {0x1B150, 0x1B152}, {0x1B155, 0x1B155}, {0x1B164, 0x1B167},
	{0x1B170, 0x1B2FB}, {0x1F004, 0x1F004}, {0x1F0CF, 0x1F0CF},
	{0x1F18E, 0x1F18E}, {0x1F191, 0x1F19A}, {0x1F200, 0x1F202}, {0x1F210, 0x1F23B}, {0x1F240, 0x1F248},
	{0x1F250, 0x1F251}, {0x1F260, 0x1F265}, {0x1F300, 0x1F320}, {0x1F32D, 0x1F335}, {0x1F337, 0x1F37C},
	{0x1F37E, 0x1F393}, {0x1F3A0, 0x1F3CA}, {0x1F3CF, 0x1F3D3}, {0x1F3E0, 0x1F3F0}, {0x1F3F4, 0x1F3F4},
	{0x1F3F8, 0x1F43E}, {0x1F440, 0x1F440}, {0x1F442, 0x1F4FC}, {0x1F4FF, 0x1F53D}, {0x1F54B, 0x1F54E},
	{0x1F550, 0x1F567}, {0x1F57A, 0x1F57A}, {0x1F595, 0x1F596}, {0x1F5A4, 0x1F5A4}, {0x1F5FB, 0x1F64F},
	{0x1F680, 0x1F6C5}, {0x1F6CC, 0x1F6CC}, {0x1F6D0, 0x1F6D2}, {0x1F6D5, 0x1F6D7}, {0x1F6DC, 0x1F6DF},
	{0x1F6EB, 0x1F6EC}, {0x1F6F4, 0x1F6FC}, {0x1F7E0, 0x1F7EB}, {0x1F7F0, 0x1F7F0}, {0x1F90C, 0x1F93A},
	{0x1F93C, 0x1F945}, {0x1F947, 0x1F9FF}, {0x1FA70, 0x1FA7C}, {0x1FA80, 0x1FA88}, {0x1FA90, 0x1FABD},
	{0x1FABF, 0x1FAC5}, {0x1FACE, 0x1FADB}, {0x1FAE0, 0x1FAE8}, {0x1FAF0, 0x1FAF8}, {0x20000, 0x2FFFD},
	{0x30000, 0x3FFFD},
}

// wide reports whether c is East Asian wide or fullwidth (the ideographic space U+3000 and the
// fullwidth forms included).
func wide(c rune) bool {
	if c < 0x1100 {
		return false
	}
	i := sort.Search(len(wideRanges), func(i int) bool { return wideRanges[i][1] >= c })
	return i < len(wideRanges) && wideRanges[i][0] <= c
}
