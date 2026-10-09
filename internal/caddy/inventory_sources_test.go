package caddy

import "testing"

func TestInventoryImportedFileLexer(t *testing.T) {
	for _, test := range []struct {
		name, source string
		refuse       bool
	}{
		{"operator response", "hello.example.test {\n respond \"import\"\n}\n", false},
		{"comment", "# import /etc/caddy/brine/current/*.caddy\nhello.example.test {\n respond ok\n}\n", false},
		{"unused snippet", "(unused) {\n import /etc/caddy/brine/current/*.caddy\n}\n", true},
		{"quoted directive", "(unused) {\n \"import\" /etc/caddy/brine/current/*.caddy\n}\n", true},
		{"inline quoted directive", "(unused) { \"import\" /etc/caddy/brine/current/*.caddy }\n", true},
		{"environment expansion", "{$CONTENTS}\n", true},
		{"unterminated quote", "\"import\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateInventoryFile([]byte(test.source)); (err != nil) != test.refuse {
				t.Fatalf("refusal %v, expected %v", err, test.refuse)
			}
		})
	}
}
