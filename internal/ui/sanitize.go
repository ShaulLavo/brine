// Package ui provides terminal presentation helpers.
package ui

import (
	"strings"
	"unicode"
)

// Sanitize replaces terminal control and formatting characters with spaces.
func Sanitize(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, text)
}
