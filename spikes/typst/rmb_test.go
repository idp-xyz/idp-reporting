package main

import "testing"

func TestRMBUpper(t *testing.T) {
	for in, want := range map[string]string{
		"0.00":                "零元整",
		"0.05":                "伍分",
		"0.50":                "伍角整",
		"10.00":               "壹拾元整",
		"1010.00":             "壹仟零壹拾元整",
		"1000.05":             "壹仟元零伍分",
		"100005.00":           "壹拾万零伍元整",
		"10000000.00":         "壹仟万元整",
		"100010000.00":        "壹亿零壹万元整",
		"1234567.89":          "壹佰贰拾叁万肆仟伍佰陆拾柒元捌角玖分",
		"-20.00":              "负贰拾元整",
		"-0.00":               "零元整",
		"9999999999999999.99": "玖仟玖佰玖拾玖万亿玖仟玖佰玖拾玖亿玖仟玖佰玖拾玖万玖仟玖佰玖拾玖元玖角玖分",
	} {
		if got, e := rmbUpper(in); e != nil || got != want {
			t.Errorf("rmbUpper(%q) = %q, %v; want %q", in, got, e, want)
		}
	}
	for _, in := range []string{"12345678901234567.00", "1.5", "1", "abc", "1e5", "", "+1.00", "1.000", "-.50"} {
		if got, e := rmbUpper(in); e == nil {
			t.Errorf("rmbUpper(%q) = %q; want an error", in, got)
		}
	}
}
