package reporting_test

import (
	r "idp.local/reporting/reporting"
	"strings"
	"testing"
)

// ValidIssueNumber is the contract of the product's number: 1..32 of A-Za-z0-9._-, starting with a
// letter or a digit. Every character class is tried at the first place and after it.
func TestValidIssueNumber(t *testing.T) {
	allowedAfterTheFirst := func(c rune) bool { return c == '.' || c == '_' || c == '-' }
	alnum := func(c rune) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' }
	check := func(label, n string, want bool) {
		t.Helper()
		if got := r.ValidIssueNumber(n); got != want {
			t.Errorf("%s %q: %v, want %v", label, n, got, want)
		}
	}
	// Every ASCII character, at index 0 and after the first, in the middle and at the end.
	for c := rune(0); c < 0x80; c++ {
		s := string(c)
		check("first", s+"1", alnum(c))
		check("alone", s, alnum(c))
		check("second", "A"+s, alnum(c) || allowedAfterTheFirst(c))
		check("middle", "A"+s+"B", alnum(c) || allowedAfterTheFirst(c))
		check("last", "AR-000001"+s, alnum(c) || allowedAfterTheFirst(c))
	}
	// The boundaries of the three ranges, and the characters on either side of them.
	for _, n := range []string{"A", "Z", "a", "z", "0", "9", "A9", "Zz", "a0", "09"} {
		check("boundary", n, true)
	}
	for _, c := range []string{"@", "[", "`", "{", "/", ":", "\\", "^", "|", "~"} {
		check("beside a range, first", c+"1", false)
		check("beside a range, second", "A"+c, false)
	}
	// Controls, DEL, the space and the line ends that a regular expression's `$` would let by.
	for _, n := range []string{"", " ", " A", "A ", "A B", "\tA", "A\t", "\nA", "AR-1\n", "AR-1\r\n", "A\x00", "\x00", "A\x7f", "\x7fA"} {
		check("blank or control", n, false)
	}
	// Anything beyond ASCII: accents, ideographs, the full-width and Cyrillic look-alikes of letters
	// and digits, a bidi mark, a zero-width space, and bytes that are not UTF-8.
	for _, n := range []string{"é", "Aé", "éA", "中", "A中", "中A", "ＡＢ", "AＢ", "１２", "A１", "А", "АR-1", "AR-А1", "Α1", "A\u202e", "A\u200b", "A\u00a0", "\xff", "A\xff", "A\xc3"} {
		check("not ASCII", n, false)
	}
	// The lengths: 1 and 32 are the limits, 33 is over, even of valid characters; the empty string is none.
	check("length 1", "A", true)
	check("length 32", strings.Repeat("A", 32), true)
	check("length 32, mixed", "A"+strings.Repeat("-._", 10)+"z", true)
	check("length 33", strings.Repeat("A", 33), false)
	check("length 33, mixed", "A"+strings.Repeat("-._", 10)+"zz", false)
	check("empty", "", false)
	check("32 bytes that are not 32 characters", strings.Repeat("é", 16), false)
	// What the simulators and the contract examples print.
	for _, n := range []string{"AR-000001", "SH-999999", "DOC-1234567", "a.b_c-d", "000012", "A-", "A..", strings.Repeat("9", 32)} {
		check("example", n, true)
	}
}
