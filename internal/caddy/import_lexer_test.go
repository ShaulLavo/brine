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
	"reflect"
	"strings"
	"testing"
)

// Text and logical-line expectations follow Caddy v2.6.2's lexer_test.go.
// These are pinned semantics, including whitespace-delimited braces and #.
func TestPinnedCaddyLexerSemantics(t *testing.T) {
	for _, test := range []struct {
		input string
		text  []string
		lines []int
	}{
		{"host:123", []string{"host:123"}, []int{1}},
		{"host:123\n\n directive", []string{"host:123", "directive"}, []int{1, 3}},
		{"host:123 { directive }", []string{"host:123", "{", "directive", "}"}, []int{1, 1, 1, 1}},
		{"host:123 {\n# comment\nfoobar # comment\n}", []string{"host:123", "{", "foobar", "}"}, []int{1, 1, 3, 4}},
		{"redir / /some/#/path", []string{"redir", "/", "/some/#/path"}, []int{1, 1, 1}},
		{`a "quoted value" b`, []string{"a", "quoted value", "b"}, []int{1, 1, 1}},
		{`A "quoted \"value\" inside" B`, []string{"A", `quoted "value" inside`, "B"}, []int{1, 1, 1}},
		{"An escaped \"newline\\\ninside\" quotes", []string{"An", "escaped", "newline\\\ninside", "quotes"}, []int{1, 1, 1, 2}},
		{"An escaped newline\\\noutside quotes", []string{"An", "escaped", "newline", "outside", "quotes"}, []int{1, 1, 1, 1, 1}},
		{"line1\\\nescaped\nline2\nline3", []string{"line1", "escaped", "line2", "line3"}, []int{1, 1, 3, 4}},
		{"line1\\\nescaped1\\\nescaped2\nline4\nline5", []string{"line1", "escaped1", "escaped2", "line4", "line5"}, []int{1, 1, 1, 4, 5}},
		{`"don't\escape"`, []string{`don't\escape`}, []int{1}},
		{`"don't\\escape"`, []string{`don't\\escape`}, []int{1}},
		{`un\escapable`, []string{`un\escapable`}, []int{1}},
		{"A \"quoted value with line\nbreak inside\" {\nfoobar\n}", []string{"A", "quoted value with line\nbreak inside", "{", "foobar", "}"}, []int{1, 1, 2, 3, 4}},
		{`"C:\php\php-cgi.exe"`, []string{`C:\php\php-cgi.exe`}, []int{1}},
		{`empty "" string`, []string{"empty", "", "string"}, []int{1, 1, 1}},
		{"skip those\r\nCR characters", []string{"skip", "those", "CR", "characters"}, []int{1, 1, 2, 2}},
		{"\xef\xbb\xbf:8080", []string{":8080"}, []int{1}},
		{"simple `backtick quoted` string", []string{"simple", "backtick quoted", "string"}, []int{1, 1, 1}},
		{"multiline `backtick\nquoted\n` string", []string{"multiline", "backtick\nquoted\n", "string"}, []int{1, 1, 3}},
		{"nested `\"quotes inside\" backticks` string", []string{"nested", `"quotes inside" backticks`, "string"}, []int{1, 1, 1}},
		{"reverse-nested \"`backticks` inside\" quotes", []string{"reverse-nested", "`backticks` inside", "quotes"}, []int{1, 1, 1}},
		{"foo{\n}", []string{"foo{", "}"}, []int{1, 2}},
	} {
		tokens, err := rootTokens([]byte(test.input))
		if err != nil {
			t.Fatal(err)
		}
		text, lines := []string{}, []int{}
		previousEnd := 0
		for _, token := range tokens {
			text = append(text, token.text)
			lines = append(lines, token.line)
			if token.start < previousEnd || token.end < token.start || token.end > len(test.input) {
				t.Fatalf("bad span %+v in %q", token, test.input)
			}
			source := test.input[token.start:token.end]
			if token.quote != 0 && (source[0] != byte(token.quote) || source[len(source)-1] != byte(token.quote)) {
				t.Fatalf("quote span %q", source)
			}
			previousEnd = token.end
		}
		if !reflect.DeepEqual(text, test.text) || !reflect.DeepEqual(lines, test.lines) {
			t.Fatalf("input %q\ngot %q %v\nwant %q %v", test.input, text, lines, test.text, test.lines)
		}
	}
}

func FuzzCandidateRoot(f *testing.F) {
	for _, seed := range []string{"import", "hello\nimport /etc/caddy/brine/current/*.caddy\nworld", "# import", "{ }", "\\\nimport", "", "\r\n"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		root := "/etc/caddy/brine"
		// Backtick content cannot close its enclosing token, so its bytes must stay
		// unchanged even when they resemble imports, comments, braces or newlines.
		body = strings.ReplaceAll(body, "`", "")
		prefix := ":8080 {\n respond `" + body + "`\n}\n"
		main := []byte(prefix + "import " + root + "/current/*.caddy\n")
		got, err := candidateRoot(main, root, "gen-2")
		if err != nil {
			return
		}
		want := prefix + "import " + root + "/gen-2/*.caddy\n"
		if string(got) != want {
			t.Fatal("operator content modified")
		}
		if _, err := candidateRoot([]byte(prefix), root, "gen-2"); err == nil {
			t.Fatal("quoted import mistaken for directive")
		}
	})
}
