// Package barcode encodes a value into the module matrix of a Code 128 or QR symbol (ADR 0005, S03-M). It is the one
// place the third-party encoder, github.com/boombuler/barcode (MIT, pinned in go.mod and go.sum), enters the formal render
// path, and it is wrapped so that the library can be replaced: nothing else imports it.
//
// The package encodes and nothing else. A symbol comes out as booleans, true for a dark module; the layout
// (export/pdf) turns the modules into vector rectangles at exact positions, with the quiet zone and the module size
// rules of the ADR, and Typst draws them. A value outside the symbology's alphabet or length is refused, never altered
// or cut, with an error that names the rule and never carries the value.
package barcode

import (
	"errors"
	"strings"
	"unicode/utf8"

	bb "github.com/boombuler/barcode"
	"github.com/boombuler/barcode/code128"
	"github.com/boombuler/barcode/qr"
)

// The limits of ADR 0005, decision 3 and 4, and the sizes of the quiet zones in modules.
const (
	// Code128MaxRunes and QRMaxBytes bound a value; the least is one character (one byte).
	Code128MaxRunes = 64
	QRMaxBytes      = 256

	// Code128Quiet and QRQuiet are the quiet zones, in modules, on each side of a symbol that lies inside its box.
	Code128Quiet = 10
	QRQuiet      = 4

	// Code128MinModule and QRMinModule are the narrowest module, in millimetres, a symbol may be drawn with.
	Code128MinModule = 0.25
	QRMinModule      = 0.4

	// MaxIssueNumber is the longest number the source product may assign (reporting.ValidIssueNumber).
	MaxIssueNumber = 32

	// PreviewNumber is what every preview prints, and encodes, in place of the issue number.
	PreviewNumber = "PREVIEW-NUMBER"
)

// IssueNumberAlphabet is the alphabet of an issue number: A-Z a-z 0-9 . _ -
const IssueNumberAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789._-"

// The ways a value is refused. The errors say which rule, never the value.
var (
	// ErrNotCovered: a character outside the symbology's alphabet, or no character at all.
	ErrNotCovered = errors.New("barcode: the value holds a character the symbology cannot encode, or none")
	// ErrTooLong: more characters or bytes than the symbology takes here.
	ErrTooLong = errors.New("barcode: the value is longer than the symbology takes")
	// ErrECC: an error correction level other than L, M, Q or H.
	ErrECC = errors.New("barcode: unknown error correction level")
	// ErrEncoder: the encoder failed or produced a symbol that is not the shape its symbology has; the wrapper never draws it.
	ErrEncoder = errors.New("barcode: the encoder produced no valid symbol")
)

// CheckCode128 reports whether value is one a Code 128 symbol takes: 1 to 64 characters, each printable ASCII (32 to 126).
func CheckCode128(value string) error {
	if value == "" {
		return ErrNotCovered
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 32 || value[i] > 126 {
			return ErrNotCovered
		}
	}
	if len(value) > Code128MaxRunes {
		return ErrTooLong
	}
	return nil
}

// CheckQR reports whether value is one a QR symbol takes: valid UTF-8 without control characters, 1 to 256 bytes.
func CheckQR(value string) error {
	if value == "" || !utf8.ValidString(value) {
		return ErrNotCovered
	}
	for _, c := range value {
		if c < 32 || c == 0x7F {
			return ErrNotCovered
		}
	}
	if len(value) > QRMaxBytes {
		return ErrTooLong
	}
	return nil
}

// ValidECC reports whether ecc names an error correction level.
func ValidECC(ecc string) bool { _, ok := levels[ecc]; return ok }

var levels = map[string]qr.ErrorCorrectionLevel{"L": qr.L, "M": qr.M, "Q": qr.Q, "H": qr.H}

// dark reports whether the module of an encoded symbol at (x, y) is dark.
func dark(c bb.Barcode, x, y int) bool {
	r, g, b, _ := c.At(x, y).RGBA()
	return r == 0 && g == 0 && b == 0
}

// The 11-module patterns of the three start symbols and the 13-module stop pattern of Code 128.
var (
	startPatterns = []string{"11010000100", "11010010000", "11010011100"}
	stopPattern   = "1100011101011"
)

// Code128 encodes value as a Code 128 symbol with automatic code sets and returns its modules from left to right, true for
// a bar. The symbol has no quiet zone. A value outside ASCII 32 to 126 or longer than 64 characters is refused.
func Code128(value string) ([]bool, error) {
	if err := CheckCode128(value); err != nil {
		return nil, err
	}
	sym, err := code128.Encode(value)
	if err != nil || sym == nil {
		return nil, ErrEncoder
	}
	b := sym.Bounds()
	if b.Min.X != 0 || b.Min.Y != 0 || b.Dy() != 1 {
		return nil, ErrEncoder
	}
	modules := make([]bool, b.Dx())
	var pattern strings.Builder
	for x := range modules {
		modules[x] = dark(sym, x, 0)
		if modules[x] {
			pattern.WriteByte('1')
		} else {
			pattern.WriteByte('0')
		}
	}
	// Shape: a start symbol (11 modules), data and check symbols (11 each), the stop (13): at least start, one value, check and stop.
	s := pattern.String()
	n := len(s)
	if n < 11*3+13 || (n-13)%11 != 0 || !contains(startPatterns, s[:11]) || s[n-13:] != stopPattern {
		return nil, ErrEncoder
	}
	return modules, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// QR encodes value as a QR symbol in byte mode (UTF-8, no ECI header) at the error correction level ecc (L, M, Q or H) and
// returns its modules, row by row from the top, true for a dark module. The symbol has no quiet zone. A value that is not
// valid UTF-8, holds a control character, is empty or is longer than 256 bytes is refused.
func QR(value, ecc string) ([][]bool, error) {
	level, ok := levels[ecc]
	if !ok {
		return nil, ErrECC
	}
	if err := CheckQR(value); err != nil {
		return nil, err
	}
	sym, err := qr.Encode(value, level, qr.Unicode)
	if err != nil || sym == nil {
		return nil, ErrEncoder
	}
	b := sym.Bounds()
	n := b.Dx()
	// A version from 1 to 40: 21 to 177 modules, 4 more for each.
	if b.Min.X != 0 || b.Min.Y != 0 || b.Dy() != n || n < 21 || n > 177 || (n-21)%4 != 0 {
		return nil, ErrEncoder
	}
	matrix := make([][]bool, n)
	for y := range matrix {
		matrix[y] = make([]bool, n)
		for x := range matrix[y] {
			matrix[y][x] = dark(sym, x, y)
		}
	}
	// The three finder patterns are what the symbol is found by: a seven-module dark ring around a three-module dark square.
	for _, c := range [][2]int{{0, 0}, {0, n - 7}, {n - 7, 0}} {
		for dy := 0; dy < 7; dy++ {
			for dx := 0; dx < 7; dx++ {
				want := dx == 0 || dx == 6 || dy == 0 || dy == 6 || (dx >= 2 && dx <= 4 && dy >= 2 && dy <= 4)
				if matrix[c[1]+dy][c[0]+dx] != want {
					return nil, ErrEncoder
				}
			}
		}
	}
	return matrix, nil
}

// Run is a run of dark modules: Start is the first module and Len the number of modules.
type Run struct{ Start, Len int }

// Runs merges consecutive dark modules into runs, from left to right: the bars of a Code 128 symbol, or the rectangles of one
// row of a QR symbol.
func Runs(modules []bool) []Run {
	var out []Run
	for i := 0; i < len(modules); i++ {
		if !modules[i] {
			continue
		}
		j := i
		for j < len(modules) && modules[j] {
			j++
		}
		out = append(out, Run{i, j - i})
		i = j - 1
	}
	return out
}

// Code128Modules returns the number of modules the symbol of value has, without its quiet zone.
func Code128Modules(value string) (int, error) {
	m, err := Code128(value)
	return len(m), err
}

// QRDimension returns the modules on a side of the symbol that holds n bytes at the error correction level ecc, without the
// quiet zone: in byte mode it depends on the length and the level alone.
func QRDimension(n int, ecc string) (int, error) {
	if n < 1 || n > QRMaxBytes {
		return 0, ErrTooLong
	}
	m, err := QR(strings.Repeat("0", n), ecc)
	return len(m), err
}

// IssueNumberCode128Modules returns the most modules (without the quiet zone) the Code 128 symbol of a legal issue number can
// have: 1 to 32 characters of IssueNumberAlphabet. The code set is chosen by the encoder, and for this alphabet it is Code B,
// with Code C only for a run of at least four digits; entering Code C costs one symbol and saves one for every pair after
// the first, and leaving it costs one, so no number needs more symbols than characters. The worst case is therefore 32
// symbols, which 32 characters that are not digits need exactly (a start symbol, 32 data symbols, the check symbol and the
// stop). The test of the package searches every pattern of digits and other characters up to a length and a random sample at 32.
func IssueNumberCode128Modules() (int, error) {
	return Code128Modules(strings.Repeat("W", MaxIssueNumber))
}
