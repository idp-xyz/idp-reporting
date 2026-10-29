package reporting_test

import (
	r "idp.local/reporting/reporting"
	"strings"
	"testing"
	"time"
)

// A reference the product gives is 1..128 printable ASCII characters, 0x21 to 0x7E: the platform keeps it
// in the record, prints it and gives it back, so it must be the same string everywhere.
func TestValidProductRef(t *testing.T) {
	for c := 0; c < 0x100; c++ {
		s := string(rune(c))
		if c >= 0x80 {
			s = string([]byte{byte(c)}) // a byte that is not ASCII, alone
		}
		want := c >= 0x21 && c <= 0x7e
		if got := r.ValidProductRef(s); got != want {
			t.Errorf("byte %#02x: %v, want %v", c, got, want)
		}
		if got := r.ValidProductRef("a" + s + "b"); got != want {
			t.Errorf("byte %#02x inside: %v, want %v", c, got, want)
		}
	}
	for ref, want := range map[string]bool{
		"": false, "x": true, strings.Repeat("x", 128): true, strings.Repeat("x", 129): false, strings.Repeat("~", 128): true,
		" ": false, "a b": false, " a": false, "a ": false, "\x7f": false, "a\x00b": false, "é": false, "中": false, "ａ": false,
		"a\u202eb": false, "a\u200bb": false, "a\u0085b": false, "\xff": false, "a\xc3": false, "ab\n": false, "ab\r\n": false,
		"RES-3": true, "0a1b2c3d4e5f60718293a4b5c6d7e8f9": true, "a/b?c=d&e#f": true,
	} {
		if got := r.ValidProductRef(ref); got != want {
			t.Errorf("%q: %v, want %v", ref, got, want)
		}
	}
}

// A product's time is a whole second between 1970-01-01T00:00:00Z and 9999-12-31T23:59:59Z, judged by
// value in any zone.
func TestValidProductTime(t *testing.T) {
	zone := time.FixedZone("far", 14*3600)
	first, last := time.Unix(0, 0).UTC(), time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	for name, c := range map[string]struct {
		at   time.Time
		want bool
	}{
		"the zero time":                    {time.Time{}, false},
		"the first second":                 {first, true},
		"one second before it":             {first.Add(-time.Second), false},
		"the first second in another zone": {first.In(zone), true},
		"the last second":                  {last, true},
		"the last second in another zone":  {last.In(zone), true},
		"one second after it":              {last.Add(time.Second), false},
		"the year 10000":                   {time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false},
		"the year 12345":                   {time.Date(12345, 6, 7, 8, 9, 10, 0, time.UTC), false},
		"a negative year":                  {time.Date(-5, 1, 1, 0, 0, 0, 0, time.UTC), false},
		"the year 1969":                    {time.Date(1969, 7, 20, 20, 17, 40, 0, time.UTC), false},
		"a fraction of a second":           {first.Add(time.Millisecond), false},
		"a nanosecond":                     {last.Add(-time.Nanosecond), false},
		"an ordinary time":                 {time.Date(2026, 10, 28, 6, 12, 3, 0, time.UTC), true},
		"the same time in another zone":    {time.Date(2026, 10, 28, 14, 12, 3, 0, time.FixedZone("CST", 8*3600)), true},
		"half a minute's offset, a second": {time.Date(2026, 10, 28, 6, 12, 3, 0, time.FixedZone("odd", 5*3600+30*60)), true},
	} {
		if got := r.ValidProductTime(c.at); got != c.want {
			t.Errorf("%s (%v): %v, want %v", name, c.at, got, c.want)
		}
	}
}
