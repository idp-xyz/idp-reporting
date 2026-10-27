// Package statementdata builds the deterministic sample statement that every PDF engine spike renders,
// so that engines are compared on byte-identical input. Every amount and running total is exact cents.
package statementdata

import (
	"fmt"
	"math/big"
	"math/rand/v2"
	"strings"
)

type Meta struct {
	Title    string `json:"title"`
	Company  string `json:"company"`
	Customer string `json:"customer"`
	Period   string `json:"period"`
	Snapshot string `json:"snapshot"`
	Template string `json:"template"`
	Draft    bool   `json:"draft"`
}

type Row struct {
	Date   string `json:"date"`
	Doc    string `json:"doc"`
	Desc   string `json:"desc"`
	Amount string `json:"amount"`
	Cum    string `json:"cum"`
}

type Statement struct {
	Meta       Meta   `json:"meta"`
	Rows       []Row  `json:"rows"`
	Total      string `json:"total"`
	TotalUpper string `json:"total_upper"`
}

// Options select the stress cases mixed into the generated rows.
type Options struct {
	Rows        int
	Seed        uint64
	Hostile     bool   // markup-like and script-like strings in descriptions
	Long        bool   // several punctuation-dense phrases per description
	Tall        bool   // the second row's description is taller than a page
	FirstAmount string // force the first row's amount
	Draft       bool
}

var phrases = []string{
	"销售回款",
	"按合同第3.2条约定结算货款，含运费与保险费。",
	"根据《补充协议》（编号BC-2026-017），甲方应于收到发票后30日内付款；逾期未付的，按日万分之五计收违约金。",
	"退货冲减：SKU-A12（加急）共12件，质检不合格，已签收“退货单”。",
	"预收款核销——2026年第三季度服务费，见附件1、附件2……",
	"汇率差额调整（USD→CNY），参考中国人民银行中间价7.1234。",
	"手续费",
	"华东大区重点客户·上海浦东新区张江高科技园区某某医疗器械股份有限公司第二事业部月度对账差异，经双方确认后调整。",
}

var hostiles = []string{`#panic("injected")`, `]`, `[`, `$x^2$`, `<script>alert(1)</script>`, `@label`, `*粗体*`, `_斜体_`, `\`, `#read("/etc/passwd")`, `= 标题`, "```raw```"}

// Build makes the statement for the given options.
func Build(o Options) (Statement, error) {
	rnd := rand.New(rand.NewPCG(o.Seed, o.Seed^0x9e3779b97f4a7c15))
	doc := Statement{Meta: Meta{Title: "应收账款对账单", Company: "示例集团华东有限公司", Customer: "某某医疗器械股份有限公司",
		Period: "2026-09-01 至 2026-09-30", Snapshot: fmt.Sprintf("snap-%016x", o.Seed), Template: "statement@spike-1", Draft: o.Draft}}
	sum := new(big.Int)
	for i := 0; i < o.Rows; i++ {
		cents := big.NewInt(rnd.Int64N(55_000_000) - 5_000_000)
		if i == 0 && o.FirstAmount != "" {
			c, e := ParseCents(o.FirstAmount)
			if e != nil {
				return doc, e
			}
			cents = c
		}
		desc := phrases[rnd.IntN(len(phrases))]
		for k := 2 + rnd.IntN(3); o.Long && k > 0; k-- {
			desc += phrases[rnd.IntN(len(phrases))]
		}
		if o.Tall && i == 1 {
			desc = strings.Repeat(phrases[2], 120)
		}
		if o.Hostile && i%7 == 3 {
			desc = hostiles[(i/7)%len(hostiles)] + " " + desc
		}
		sum.Add(sum, cents)
		doc.Rows = append(doc.Rows, Row{Date: fmt.Sprintf("2026-09-%02d", 1+i%30), Doc: fmt.Sprintf("AR-%06d", i+1),
			Desc: desc, Amount: FormatCents(cents), Cum: FormatCents(sum)})
	}
	doc.Total = FormatCents(sum)
	upper, e := RMBUpper(doc.Total)
	if e != nil {
		return doc, fmt.Errorf("total %s: %w", doc.Total, e)
	}
	doc.TotalUpper = upper
	return doc, nil
}

// ParseCents accepts a plain decimal with at most two fraction digits and no exponent.
func ParseCents(s string) (*big.Int, error) {
	neg := strings.HasPrefix(s, "-")
	whole, frac, _ := strings.Cut(strings.TrimPrefix(s, "-"), ".")
	if !allDigits(whole) || len(frac) > 2 || (frac != "" && !allDigits(frac)) {
		return nil, fmt.Errorf("not a plain two-decimal amount: %q", s)
	}
	v, _ := new(big.Int).SetString(whole+(frac + "00")[:2], 10)
	if neg {
		v.Neg(v)
	}
	return v, nil
}

func FormatCents(v *big.Int) string {
	s := new(big.Int).Abs(v).String()
	if len(s) < 3 {
		s = strings.Repeat("0", 3-len(s)) + s
	}
	s = s[:len(s)-2] + "." + s[len(s)-2:]
	if v.Sign() < 0 {
		return "-" + s
	}
	return s
}
