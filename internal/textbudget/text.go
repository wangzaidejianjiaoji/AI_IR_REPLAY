// Package textbudget applies byte limits without splitting UTF-8 characters.
package textbudget

import "unicode/utf8"

func Limit(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	n := max
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
