package reporting_test

import (
	"encoding/json"
	"fmt"
	"testing"

	r "idp.local/reporting/reporting"
)

// What the cell costs against the string it was (docs/S26A_SCOPE.md "Known"): a thousand rows of twenty columns, four of them
// names and sixteen numbers, encoded, decoded and digested. Run with
//
//	go test -run '^$' -bench BenchmarkRowsEncoding -benchmem ./reporting/
func benchRows(cjk bool) [][]string {
	rows := make([][]string, 1000)
	for i := range rows {
		row := make([]string, 20)
		for j := range row {
			switch {
			case j < 4 && cjk:
				row[j] = fmt.Sprintf("客户名称-%d-%d", i, j)
			case j < 4:
				row[j] = fmt.Sprintf("customer_%d_%d", i, j)
			default:
				row[j] = fmt.Sprintf("%d.%02d", i*1000+j, j)
			}
		}
		rows[i] = row
	}
	return rows
}

func BenchmarkRowsEncoding(b *testing.B) {
	for _, cjk := range []bool{false, true} {
		rows := benchRows(cjk)
		cells := r.CellRows(rows)
		text, _ := json.Marshal(rows)
		name := map[bool]string{false: "ascii", true: "cjk"}[cjk]
		b.Run(name+"/marshal/strings", func(b *testing.B) {
			for b.Loop() {
				json.Marshal(rows)
			}
		})
		b.Run(name+"/marshal/cells", func(b *testing.B) {
			for b.Loop() {
				json.Marshal(cells)
			}
		})
		b.Run(name+"/unmarshal/strings", func(b *testing.B) {
			for b.Loop() {
				var out [][]string
				json.Unmarshal(text, &out)
			}
		})
		b.Run(name+"/unmarshal/cells", func(b *testing.B) {
			for b.Loop() {
				var out [][]r.Cell
				json.Unmarshal(text, &out)
			}
		})
		b.Run(name+"/digest/cells", func(b *testing.B) {
			for b.Loop() {
				r.RowsDigest(cells)
			}
		})
	}
}
