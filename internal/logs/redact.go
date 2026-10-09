package logs

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	privateBegin = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----`)
	privateEnd   = regexp.MustCompile(`-----END (?:[A-Z0-9]+ )*PRIVATE KEY-----`)
	secretPair   = regexp.MustCompile(`(?i)([a-z0-9_.-]*(?:secret|password|passwd|token|api[_-]?key|access[_-]?key|private[_-]?key|credential|authorization)[a-z0-9_.-]*["']?\s*[:=]\s*)(?:bearer\s+[^\s,;]+|"(?:\\[^\n]|[^"\\\n])*(?:"|\n|$)|'(?:\\[^\n]|[^'\\\n])*(?:'|\n|$)|[^\s,;]+)`)
	bearer       = regexp.MustCompile(`(?i)(bearer\s+)[^\s,"';]+`)
	longToken    = regexp.MustCompile(`[A-Za-z0-9_+/=-]{32,}`)
	urlPassword  = regexp.MustCompile(`(://[^\s:/]+:)[^\s@/]+(@)`)
)

type redactor struct{ private bool }

func (r *redactor) clean(input string) string {
	text := stripANSI(input)
	var out strings.Builder
	for len(text) > 0 {
		if r.private {
			end := privateEnd.FindStringIndex(text)
			if end == nil {
				out.WriteString("[REDACTED]")
				text = ""
				break
			}
			out.WriteString("[REDACTED]")
			text = text[end[1]:]
			r.private = false
			continue
		}
		begin := privateBegin.FindStringIndex(text)
		if begin == nil {
			out.WriteString(text)
			break
		}
		out.WriteString(text[:begin[0]])
		text = text[begin[1]:]
		r.private = true
	}
	if r.private && !strings.HasSuffix(out.String(), "[REDACTED]") {
		out.WriteString("[REDACTED]")
	}
	text = out.String()
	text = secretPair.ReplaceAllString(text, "${1}[REDACTED]")
	text = bearer.ReplaceAllString(text, "${1}[REDACTED]")
	text = urlPassword.ReplaceAllString(text, "${1}[REDACTED]${2}")
	return longToken.ReplaceAllString(text, "[REDACTED]")
}
func stripANSI(s string) string {
	var b strings.Builder
	state := byte(0)
	for _, r := range strings.ToValidUTF8(s, "\uFFFD") {
		if r == '\n' {
			state = 0
			b.WriteRune(r)
			continue
		}
		switch state {
		case 0:
			switch r {
			case 0x1b:
				state = 1
			case 0x9b:
				state = 2
			case 0x9d, 0x90, 0x98, 0x9e, 0x9f:
				state = 3
			default:
				if !unicode.IsControl(r) || r == '\n' || r == '\t' {
					b.WriteRune(r)
				}
			}
		case 1:
			switch r {
			case '[':
				state = 2
			case ']', 'P', 'X', '^', '_':
				state = 3
			default:
				if r >= 0x20 && r <= 0x2f {
					state = 5
				} else {
					state = 0
				}
			}
		case 2:
			if r >= 0x40 && r <= 0x7e {
				state = 0
			}
		case 3:
			if r == 7 || r == 0x9c {
				state = 0
			} else if r == 0x1b {
				state = 4
			}
		case 5:
			if r >= 0x30 && r <= 0x7e {
				state = 0
			}
		case 4:
			if r == '\\' {
				state = 0
			} else {
				state = 3
			}
		}
	}
	return b.String()
}
