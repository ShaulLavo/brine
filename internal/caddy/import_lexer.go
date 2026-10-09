// Copyright 2015 Matthew Holt and The Caddy Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package caddy

import (
	"bytes"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Adapted from Caddy v2.6.2 caddyconfig/caddyfile/lexer.go. Source byte spans
// are added for replacement without reformatting. Unterminated strings and
// heredoc markers fail closed; heredocs were introduced after the pinned 2.6.
type rootToken struct {
	text       string
	line       int
	quote      rune
	start, end int
}

func rootTokens(input []byte) ([]rootToken, error) {
	if !utf8.Valid(input) {
		return nil, errors.New("caddy: invalid root encoding")
	}
	pos := 0
	if bytes.HasPrefix(input, []byte{0xef, 0xbb, 0xbf}) {
		pos = 3
	}
	line, skipped := 1, 0
	var tokens []rootToken
	for pos < len(input) {
		var value []rune
		var comment, escaped bool
		var quote rune
		start, tokenLine := -1, 0
		emit := func(end int) error {
			text := string(value)
			if quote == 0 {
				for end > start && input[end-1] == '\r' {
					end--
				}
			}
			if quote == 0 && strings.HasPrefix(text, "<<") {
				return errors.New("caddy: heredocs require an unsupported Caddy version")
			}
			tokens = append(tokens, rootToken{text: text, line: tokenLine, quote: quote, start: start, end: end})
			return nil
		}
		emitted := false
		for pos < len(input) {
			at := pos
			ch, size := utf8.DecodeRune(input[pos:])
			pos += size
			if !escaped && quote != '`' && ch == '\\' {
				escaped = true
				if start == -1 && !comment {
					start = at
					tokenLine = line
				}
				continue
			}
			if quote != 0 {
				if quote == '"' && escaped {
					if ch != '"' {
						value = append(value, '\\')
					}
					escaped = false
				} else if ch == quote {
					if err := emit(pos); err != nil {
						return nil, err
					}
					emitted = true
					break
				}
				if ch == '\n' {
					line += 1 + skipped
					skipped = 0
				}
				value = append(value, ch)
				continue
			}
			if unicode.IsSpace(ch) {
				if ch == '\r' {
					continue
				}
				if ch == '\n' {
					if escaped {
						skipped++
						escaped = false
					} else {
						line += 1 + skipped
						skipped = 0
					}
					comment = false
				}
				if len(value) > 0 {
					if err := emit(at); err != nil {
						return nil, err
					}
					emitted = true
					break
				}
				if !escaped {
					start = -1
				}
				continue
			}
			if ch == '#' && len(value) == 0 {
				comment = true
			}
			if comment {
				continue
			}
			if len(value) == 0 {
				if start == -1 {
					start = at
					tokenLine = line
				}
				if ch == '"' || ch == '`' {
					quote = ch
					continue
				}
			}
			if escaped {
				value = append(value, '\\')
				escaped = false
			}
			value = append(value, ch)
		}
		if !emitted {
			if quote != 0 {
				return nil, errors.New("caddy: unterminated root token")
			}
			if len(value) > 0 {
				if err := emit(pos); err != nil {
					return nil, err
				}
			}
		}
	}
	return tokens, nil
}
