package barcode

import (
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/boombuler/barcode/code128"
	"github.com/boombuler/barcode/qr"
)

// code128Widths is the table of the standard, written independently of the library: for each symbol value 0 to 105, the widths
// of its three bars and three spaces in modules; the stop symbol has seven elements.
var code128Widths = []string{
	"212222", "222122", "222221", "121223", "121322", "131222", "122213", "122312", "132212", "221213",
	"221312", "231212", "112232", "122132", "122231", "113222", "123122", "123221", "223211", "221132",
	"221231", "213212", "223112", "312131", "311222", "321122", "321221", "312212", "322112", "322211",
	"212123", "212321", "232121", "111323", "131123", "131321", "112313", "132113", "132311", "211313",
	"231113", "231311", "112133", "112331", "132131", "113123", "113321", "133121", "313121", "211331",
	"231131", "213113", "213311", "213131", "311123", "311321", "331121", "312113", "312311", "332111",
	"314111", "221411", "431111", "111224", "111422", "121124", "121421", "141122", "141221", "112214",
	"112412", "122114", "122411", "142112", "142211", "241211", "221114", "413111", "241112", "134111",
	"111242", "121142", "121241", "114212", "124112", "124211", "411212", "421112", "421211", "212141",
	"214121", "412121", "111143", "111341", "131141", "114113", "114311", "411113", "411311", "113141",
	"114131", "311141", "411131", "211412", "211214", "211232",
}

const stopWidths = "2331112"

// pattern turns widths into modules, a bar first.
func pattern(widths string) []bool {
	var out []bool
	for i, w := range widths {
		for range int(w - '0') {
			out = append(out, i%2 == 0)
		}
	}
	return out
}

// expectSymbol builds the module pattern of a sequence of symbol values (the start symbol first), the check symbol and the stop.
func expectSymbol(values ...int) []bool {
	sum := values[0]
	for i, v := range values[1:] {
		sum += (i + 1) * v
	}
	var out []bool
	for _, v := range append(values, sum%103) {
		out = append(out, pattern(code128Widths[v])...)
	}
	return append(out, pattern(stopWidths)...)
}

func equalModules(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTheStandardsTableIsWellFormed(t *testing.T) {
	// Every symbol is 11 modules wide and the stop 13: a slip in the table above would show here first.
	for v, w := range code128Widths {
		if got := len(pattern(w)); got != 11 {
			t.Errorf("symbol %d: %d modules", v, got)
		}
	}
	if len(code128Widths) != 106 || len(pattern(stopWidths)) != 13 {
		t.Errorf("%d symbols, stop %d modules", len(code128Widths), len(pattern(stopWidths)))
	}
	seen := map[string]int{}
	for v := range code128Widths {
		key := string(rune(0))
		for _, m := range pattern(code128Widths[v]) {
			if m {
				key += "1"
			} else {
				key += "0"
			}
		}
		if other, ok := seen[key]; ok {
			t.Errorf("symbols %d and %d have the same pattern", other, v)
		}
		seen[key] = v
	}
}

// TestCode128MatchesTheStandard: every character of Code B as a symbol of its own after the start symbol, every digit pair of Code C,
// and mixtures that switch code sets, against the patterns of the standard.
func TestCode128MatchesTheStandard(t *testing.T) {
	const startB, startC, codeB, codeC = 104, 105, 100, 99
	for c := byte(32); c <= 126; c++ {
		got, err := Code128(string(c))
		if err != nil {
			t.Fatalf("%q: %v", c, err)
		}
		if want := expectSymbol(startB, int(c)-32); !equalModules(got, want) {
			t.Errorf("%q: modules differ from the standard's", c)
		}
	}
	// Four or more digits start in Code C; a pair is one symbol.
	for p := 0; p < 100; p++ {
		digits := "00" + string([]byte{byte('0' + p/10), byte('0' + p%10)})
		got, err := Code128(digits)
		if err != nil {
			t.Fatal(err)
		}
		if want := expectSymbol(startC, 0, p); !equalModules(got, want) {
			t.Errorf("%s: modules differ from the standard's", digits)
		}
	}
	for value, symbols := range map[string][]int{
		// Code B for letters, a switch to C for four digits and a switch back for the letter after them.
		"AB1234c": {startB, 'A' - 32, 'B' - 32, codeC, 12, 34, codeB, 'c' - 32},
		// An odd run of digits leaves its last digit to Code B.
		"12345": {startC, 12, 34, codeB, '5' - 32},
		// Three digits are not enough for Code C.
		"a123b": {startB, 'a' - 32, '1' - 32, '2' - 32, '3' - 32, 'b' - 32},
		// Digits in Code C continue with pairs.
		"123456": {startC, 12, 34, 56},
		// The longest value, 64 characters, and a value with every printable ASCII character.
		strings.Repeat("Z", 64): append([]int{startB}, repeat('Z'-32, 64)...),
	} {
		got, err := Code128(value)
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if want := expectSymbol(symbols...); !equalModules(got, want) {
			t.Errorf("%q: modules differ from the standard's", value)
		}
	}
	// Every printable character that is not a digit (a run of digits would switch to Code C), in chunks of at most 64.
	var chunk string
	symbols := []int{startB}
	flush := func() {
		got, err := Code128(chunk)
		if err != nil || !equalModules(got, expectSymbol(symbols...)) {
			t.Errorf("printable characters %q: %v", chunk, err)
		}
		chunk, symbols = "", []int{startB}
	}
	for c := byte(32); c <= 126; c++ {
		if c >= '0' && c <= '9' {
			continue
		}
		chunk += string(c)
		symbols = append(symbols, int(c)-32)
		if len(chunk) == 60 {
			flush()
		}
	}
	flush()
}

func repeat(v, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestCode128Refusals(t *testing.T) {
	long := strings.Repeat("a", 65)
	for name, c := range map[string]struct {
		value string
		want  error
	}{
		"empty":               {"", ErrNotCovered},
		"a control character": {"a\x01b", ErrNotCovered},
		"a newline":           {"a\nb", ErrNotCovered},
		"delete":              {"a\x7fb", ErrNotCovered},
		"a latin letter":      {"café", ErrNotCovered},
		"an ideograph":        {"字", ErrNotCovered},
		"a function code":     {"ñ123", ErrNotCovered},
		"invalid UTF-8":       {"a\xffb", ErrNotCovered},
		"65 characters":       {long, ErrTooLong},
	} {
		got, err := Code128(c.value)
		if err != c.want || got != nil {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
		if err != nil && c.value != "" && strings.Contains(err.Error(), strings.TrimSpace(c.value)) && len(strings.TrimSpace(c.value)) > 1 {
			t.Errorf("%s: the error carries the value: %v", name, err)
		}
	}
	if _, err := Code128(strings.Repeat("a", 64)); err != nil {
		t.Errorf("64 characters: %v", err)
	}
	if _, err := Code128(" "); err != nil {
		t.Errorf("a space: %v", err)
	}
}

func TestQRRefusals(t *testing.T) {
	for name, c := range map[string]struct {
		value, ecc string
		want       error
	}{
		"empty":               {"", "M", ErrNotCovered},
		"a control character": {"a\x01b", "M", ErrNotCovered},
		"a newline":           {"a\nb", "M", ErrNotCovered},
		"delete":              {"a\x7fb", "M", ErrNotCovered},
		"invalid UTF-8":       {"a\xffb", "M", ErrNotCovered},
		"257 bytes":           {strings.Repeat("a", 257), "L", ErrTooLong},
		"86 ideographs":       {strings.Repeat("字", 86), "L", ErrTooLong}, // 258 bytes
		"no level":            {"a", "", ErrECC},
		"a lower case level":  {"a", "m", ErrECC},
		"another level":       {"a", "X", ErrECC},
	} {
		got, err := QR(c.value, c.ecc)
		if err != c.want || got != nil {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	for name, v := range map[string]string{"256 bytes": strings.Repeat("a", 256), "85 ideographs": strings.Repeat("字", 85), "one byte": "a", "a space": " "} {
		for _, ecc := range []string{"L", "M", "Q", "H"} {
			if _, err := QR(v, ecc); err != nil {
				t.Errorf("%s at %s: %v", name, ecc, err)
			}
		}
	}
}

// dataCodewords is the number of data codewords of versions 1 to 17 at L, M, Q and H, from the standard.
var dataCodewords = [][4]int{
	{19, 16, 13, 9}, {34, 28, 22, 16}, {55, 44, 34, 26}, {80, 64, 48, 36}, {108, 86, 62, 46}, {136, 108, 76, 60},
	{156, 124, 88, 66}, {194, 154, 110, 86}, {232, 182, 132, 100}, {274, 216, 154, 122}, {324, 254, 180, 140},
	{370, 290, 206, 158}, {428, 334, 244, 180}, {461, 365, 261, 197}, {523, 415, 295, 223}, {589, 453, 325, 253}, {647, 507, 367, 283},
}

// byteCapacity is how many bytes a symbol of the version holds in byte mode: its data codewords less the 4 bits of the mode and the
// 8 bits (16 from version 10) of the count.
func byteCapacity(version, level int) int {
	d := dataCodewords[version-1][level]
	if version < 10 {
		return d - 2
	}
	return d - 3
}

func TestQRVersionFollowsTheStandardsCapacities(t *testing.T) {
	for level, ecc := range []string{"L", "M", "Q", "H"} {
		// The lengths at which the version changes: the most a version holds and one more, and the ends.
		lengths := map[int]bool{1: true, QRMaxBytes: true}
		for v := 1; v <= len(dataCodewords); v++ {
			for _, n := range []int{byteCapacity(v, level), byteCapacity(v, level) + 1} {
				if n >= 1 && n <= QRMaxBytes {
					lengths[n] = true
				}
			}
		}
		for n := range lengths {
			want := 0
			for v := 1; v <= len(dataCodewords); v++ {
				if byteCapacity(v, level) >= n {
					want = v
					break
				}
			}
			if want == 0 {
				t.Fatalf("%d bytes at %s need more than version 17", n, ecc)
			}
			got, err := QRDimension(n, ecc)
			if err != nil || got != 17+4*want {
				t.Errorf("%d bytes at %s: %d modules (%v), want version %d: %d", n, ecc, got, err, want, 17+4*want)
			}
		}
	}
}

// formatWords are the 15-bit format words of the standard: the 2 bits of the level and 3 of the mask, 10 bits of BCH, XOR 0x5412.
func formatWord(levelBits, mask int) int {
	data := levelBits<<3 | mask
	rem := data << 10
	for i := 14; i >= 10; i-- {
		if rem&(1<<i) != 0 {
			rem ^= 0x537 << (i - 10)
		}
	}
	return (data<<10 | rem) ^ 0x5412
}

// TestQRSymbolsCarryTheirLevelAndAreWellFormed checks, without the library, what a decoder needs of a symbol: the finder patterns, the
// timing patterns, the dark module and the format information (a valid codeword with the level that was asked for), twice.
func TestQRSymbolsCarryTheirLevelAndAreWellFormed(t *testing.T) {
	levelBits := map[string]int{"L": 1, "M": 0, "Q": 3, "H": 2}
	for _, value := range []string{"a", "PREVIEW-NUMBER", "https://example.invalid/pay?ref=INV-2026-0001&amount=1288.00", strings.Repeat("0123456789", 25) + "012345", "字符串"} {
		for ecc, bits := range levelBits {
			m, err := QR(value, ecc)
			if err != nil {
				t.Fatalf("%q %s: %v", value, ecc, err)
			}
			n := len(m)
			// Timing patterns alternate along row 6 and column 6 between the finder patterns.
			for i := 8; i < n-8; i++ {
				if m[6][i] != (i%2 == 0) || m[i][6] != (i%2 == 0) {
					t.Fatalf("%q %s: the timing pattern is wrong at %d", value, ecc, i)
				}
			}
			if !m[n-8][8] {
				t.Errorf("%q %s: no dark module", value, ecc)
			}
			// The format information is stored twice, most significant bit first: around the top-left finder pattern, and split between
			// the other two. Both must be one of the eight codewords of the level.
			var first, second int
			for _, p := range [][2]int{{0, 8}, {1, 8}, {2, 8}, {3, 8}, {4, 8}, {5, 8}, {7, 8}, {8, 8}, {8, 7}, {8, 5}, {8, 4}, {8, 3}, {8, 2}, {8, 1}, {8, 0}} {
				first = first<<1 | b2i(m[p[1]][p[0]]) // (column, row)
			}
			for i := 0; i < 7; i++ {
				second = second<<1 | b2i(m[n-1-i][8]) // the bottom of column 8, upwards
			}
			for i := 0; i < 8; i++ {
				second = second<<1 | b2i(m[8][n-8+i]) // the right of row 8, from the left
			}
			ok := false
			for mask := 0; mask < 8; mask++ {
				if first == formatWord(bits, mask) && second == first {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%q %s: the format information %015b and %015b is not a codeword of this level", value, ecc, first, second)
			}
			again, _ := QR(value, ecc)
			for y := range m {
				if !equalModules(m[y], again[y]) {
					t.Fatalf("%q %s: two encodings differ", value, ecc)
				}
			}
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestRuns(t *testing.T) {
	for _, c := range []struct {
		bits string
		want []Run
	}{
		{"", nil}, {"0000", nil}, {"1", []Run{{0, 1}}}, {"0110", []Run{{1, 2}}}, {"10101", []Run{{0, 1}, {2, 1}, {4, 1}}},
		{"11011100111", []Run{{0, 2}, {3, 3}, {8, 3}}}, {"1111", []Run{{0, 4}}},
	} {
		var modules []bool
		for _, b := range c.bits {
			modules = append(modules, b == '1')
		}
		got := Runs(modules)
		if len(got) != len(c.want) {
			t.Errorf("%q: %v, want %v", c.bits, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q: %v, want %v", c.bits, got, c.want)
			}
		}
	}
	// A Code 128 symbol starts and ends with a bar, and its runs hold every dark module.
	m, _ := Code128("Hello")
	runs, total := Runs(m), 0
	for _, r := range runs {
		total += r.Len
	}
	dark := 0
	for _, v := range m {
		if v {
			dark++
		}
	}
	if !m[0] || !m[len(m)-1] || total != dark {
		t.Errorf("runs %v of %d dark modules", runs, dark)
	}
}

// TestTheWorstCaseIssueNumberNeedsTheMostModules proves the bound the layout check relies on: no number of 1 to 32 characters of the
// issue alphabet needs a wider Code 128 symbol than 32 characters that are not digits. The encoder picks the code set from whether
// each character is a digit (the other characters of the alphabet are all in Code B), so every pattern of digits and other characters
// is tried up to a length, and a large random sample, characters included, at 32.
func TestTheWorstCaseIssueNumberNeedsTheMostModules(t *testing.T) {
	worst, err := IssueNumberCode128Modules()
	if err != nil || worst != 11+32*11+11+13 {
		t.Fatalf("the worst case is %d modules, want the start symbol, 32 symbols, the check and the stop: %d", worst, 11+32*11+11+13)
	}
	check := func(value string) {
		n, err := Code128Modules(value)
		if err != nil {
			t.Fatalf("%q: %v", value, err)
		}
		if n > worst {
			t.Fatalf("%q needs %d modules, more than the worst case %d", value, n, worst)
		}
		if n > 11+len(value)*11+11+13 {
			t.Fatalf("%q needs %d modules, more than a symbol a character", value, n)
		}
	}
	for length := 1; length <= 16; length++ {
		for bits := 0; bits < 1<<length; bits++ {
			var b strings.Builder
			for i := range length {
				if bits>>i&1 == 1 {
					b.WriteByte('7')
				} else {
					b.WriteByte('x')
				}
			}
			check(b.String())
		}
	}
	rnd := rand.New(rand.NewSource(1))
	for range 20000 {
		length := 1 + rnd.Intn(32)
		var b strings.Builder
		digits := rnd.Intn(4) // how digit-heavy this value is: none, some, many, only
		for range length {
			if rnd.Intn(4) < digits {
				b.WriteByte(byte('0' + rnd.Intn(10)))
			} else {
				b.WriteByte(IssueNumberAlphabet[rnd.Intn(len(IssueNumberAlphabet))])
			}
		}
		check(b.String())
	}
	// Runs of digits of every length between letters, at 32 characters.
	for run := 1; run <= 31; run++ {
		for lead := 0; lead+run <= 32; lead++ {
			v := strings.Repeat("a", lead) + strings.Repeat("5", run) + strings.Repeat("b", 32-lead-run)
			check(v)
		}
	}
}

func TestQRDimensionOfAnIssueNumberAtEachLevel(t *testing.T) {
	// 32 bytes: version 2 at L (25 modules), 3 at M and Q (29), 4 at H (33).
	for ecc, want := range map[string]int{"L": 25, "M": 29, "Q": 29, "H": 33} {
		got, err := QRDimension(MaxIssueNumber, ecc)
		if err != nil || got != want {
			t.Errorf("%s: %d modules (%v), want %d", ecc, got, err, want)
		}
	}
}

func TestEncodingIsSafeToShare(t *testing.T) {
	var wg sync.WaitGroup
	want, _ := QR("INV-2026-0001", "Q")
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				got, err := QR("INV-2026-0001", "Q")
				if err != nil || len(got) != len(want) {
					t.Error("a concurrent encoding differs", err)
					return
				}
				for y := range got {
					if !equalModules(got[y], want[y]) {
						t.Error("a concurrent encoding differs")
						return
					}
				}
				if _, err := Code128("INV-2026-0001"); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestTheLibraryBehavesAsTheWrapperAssumes records what the wrapper guards against, so that an upgrade of the library, which is
// reviewed, shows here when a guard is no longer needed or no longer enough.
func TestTheLibraryBehavesAsTheWrapperAssumes(t *testing.T) {
	// It encodes what the platform refuses: control characters and function codes in Code 128, an empty QR payload.
	if _, err := code128.Encode("a\x01b"); err != nil {
		t.Errorf("the library no longer encodes a control character: %v", err)
	}
	if _, err := code128.Encode("ñ123"); err != nil {
		t.Errorf("the library no longer encodes FNC1: %v", err)
	}
	if _, err := qr.Encode("", qr.M, qr.Unicode); err != nil {
		t.Errorf("the library no longer encodes an empty payload: %v", err)
	}
	// It refuses what it cannot encode, with an error that quotes the value (so the wrapper never passes the error on).
	if _, err := code128.Encode("café"); err == nil || !strings.Contains(err.Error(), "caf") {
		t.Errorf("the library's refusal of a letter outside Code 128: %v", err)
	}
	// It takes 80 characters of Code 128; the platform's limit is 64.
	if _, err := code128.Encode(strings.Repeat("a", 80)); err != nil {
		t.Errorf("80 characters: %v", err)
	}
	if _, err := code128.Encode(strings.Repeat("a", 81)); err == nil {
		t.Error("81 characters")
	}
	// Byte mode holds the UTF-8 bytes as they are, whatever they are.
	if _, err := qr.Encode("a\xffb", qr.M, qr.Unicode); err != nil {
		t.Errorf("the library no longer takes invalid UTF-8: %v", err)
	}
}
