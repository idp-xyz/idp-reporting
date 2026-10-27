package statementdata

import (
	"errors"
	"strings"
)

var (
	cnDigits = [...]string{"零", "壹", "贰", "叁", "肆", "伍", "陆", "柒", "捌", "玖"}
	cnUnits  = [...]string{"仟", "佰", "拾", ""}
	cnGroups = [...]string{"", "万", "亿", "万亿"}
)

// RMBUpper writes an exact two-decimal amount in capital numerals as used on vouchers.
// It refuses more than 16 integer digits rather than invent larger units.
func RMBUpper(amount string) (string, error) {
	neg := strings.HasPrefix(amount, "-")
	whole, frac, ok := strings.Cut(strings.TrimPrefix(amount, "-"), ".")
	if !ok || whole == "" || len(frac) != 2 || !allDigits(whole) || !allDigits(frac) {
		return "", errors.New("amount must be a plain decimal with exactly two fraction digits")
	}
	whole = strings.TrimLeft(whole, "0")
	if len(whole) > 16 {
		return "", errors.New("amount exceeds the 16 integer digits capital numerals support")
	}
	var b strings.Builder
	if neg && (whole != "" || frac != "00") {
		b.WriteString("负")
	}
	if whole != "" {
		padded := strings.Repeat("0", (4-len(whole)%4)%4) + whole
		groups := len(padded) / 4
		wrote, zero := false, false
		for g := 0; g < groups; g++ {
			part := padded[g*4 : g*4+4]
			if part == "0000" {
				zero = zero || wrote
				continue
			}
			for i := 0; i < 4; i++ {
				d := part[i] - '0'
				if d == 0 {
					zero = zero || wrote
					continue
				}
				if zero {
					b.WriteString("零")
					zero = false
				}
				b.WriteString(cnDigits[d] + cnUnits[i])
				wrote = true
			}
			b.WriteString(cnGroups[groups-1-g])
		}
		b.WriteString("元")
	}
	jiao, fen := frac[0]-'0', frac[1]-'0'
	switch {
	case jiao == 0 && fen == 0:
		if whole == "" {
			return "零元整", nil
		}
		b.WriteString("整")
	case jiao == 0:
		if whole != "" {
			b.WriteString("零")
		}
		b.WriteString(cnDigits[fen] + "分")
	default:
		b.WriteString(cnDigits[jiao] + "角")
		if fen == 0 {
			b.WriteString("整")
		} else {
			b.WriteString(cnDigits[fen] + "分")
		}
	}
	return b.String(), nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}
