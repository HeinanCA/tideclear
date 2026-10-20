package report

import (
	"strings"
	"unicode"
)

// plain replaces control characters in text that came from AWS or a node, so
// it cannot move the cursor, rewrite the terminal or start a forged line.
func plain(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}
