package caddy

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestQuotedTextCannotMasqueradeAsBrineImport(t *testing.T) {
	for _, quote := range []string{"`", `"`} {
		t.Run(quote, func(t *testing.T) {
			m, root, _, state, v, r := setup(t)
			main := []byte(":8080 {\n respond " + quote + "hello\nimport " + root + "/current/*.caddy\nworld" + quote + "\n}\n")
			before := diskImage(t, root)
			if _, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t))); err == nil {
				t.Fatal("quoted fake import accepted")
			}
			if !equalDisk(before, diskImage(t, root)) || len(v.paths) != 0 || r.calls != 0 {
				t.Fatal("missing import refusal touched host")
			}
		})
	}
}
func equalDisk(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, val := range a {
		if b[key] != val {
			return false
		}
	}
	return true
}

func TestImportArgumentsAreNotDirectives(t *testing.T) {
	for i, body := range []string{`"import"`, "`import`", "import", `"import /relative/*.caddy"`, "`import\n/relative/*.caddy`", "\\\nimport", `"escaped \"import\" body"`, `"# import /relative/*.caddy"`, "/some/#/import", `"" import`} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			m, root, _, state, v, r := setup(t)
			main := []byte(":8080 {\n respond " + body + "\n}\nimport " + root + "/current/*.caddy\n")
			want := bytes.Replace(main, []byte(root+"/current/*.caddy"), []byte(root+"/gen-1/*.caddy"), 1)
			if _, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t))); err != nil {
				t.Fatal("ordinary response argument refused", err)
			}
			if len(v.roots) != 1 || !bytes.Equal(v.roots[0], want) || r.calls != 1 {
				t.Fatal("operator content changed")
			}
		})
	}
}

func TestRealImportSelectedInsteadOfQuotedDecoy(t *testing.T) {
	for _, quote := range []string{"`", `"`} {
		root := "/etc/caddy/brine"
		decoy := ":8080 {\n respond " + quote + "hello\nimport " + root + "/current/*.caddy\nworld" + quote + "\n}\n"
		main := []byte(decoy + "import " + root + "/current/*.caddy\n")
		got, err := candidateRoot(main, root, "gen-2")
		if err != nil {
			t.Fatal(err)
		}
		want := []byte(decoy + "import " + root + "/gen-2/*.caddy\n")
		if !bytes.Equal(got, want) {
			t.Fatal("quoted decoy modified")
		}
	}
}

func TestBrineImportTokenSequence(t *testing.T) {
	root := "/etc/caddy/brine"
	path := root + "/current/*.caddy"
	for _, line := range []string{"import\t" + path + " # comment\n", "import " + `"` + path + `"` + "\n", "import `" + path + "`\n", "import \\\n" + path + "\n", "\xef\xbb\xbfimport " + path + "\r\n"} {
		t.Run(line, func(t *testing.T) {
			main := []byte(line)
			got, err := candidateRoot(main, root, "gen-2")
			if err != nil {
				t.Fatal(err)
			}
			want := bytes.Replace(main, []byte(path), []byte(root+"/gen-2/*.caddy"), 1)
			if !bytes.Equal(got, want) {
				t.Fatalf("got %q want %q", got, want)
			}
		})
	}
	for _, main := range []string{
		":8080 {\n import " + path + "\n}\n",
		"(unused) {\n import " + path + "\n}\n",
		"# import " + path + "\n:8080 {\n respond ok\n}\n",
		"import " + path + "\nimport \"" + path + "\"\n",
		"import " + path + " extra\n",
		"import\n" + path + "\n",
		"respond \\\nimport " + path + "\n",
	} {
		if _, err := candidateRoot([]byte(main), root, "gen-2"); err == nil {
			t.Fatalf("non-unique/top-level directive accepted %q", main)
		}
	}
}

func TestHeredocsAndEnvironmentExpansionRefuseBeforeStaging(t *testing.T) {
	for _, body := range []string{"respond <<END\nimport %ROOT%/current/*.caddy\nEND", "respond <<END\nimport %ROOT%/current/*.caddy\n", "respond {$BODY}"} {
		m, root, _, state, v, r := setup(t)
		body = strings.ReplaceAll(body, "%ROOT%", root)
		main := []byte(":8080 {\n" + body + "\n}\nimport " + root + "/current/*.caddy\n")
		if _, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t))); err == nil {
			t.Fatal("unsupported preprocessing accepted")
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 || len(v.paths) != 0 || r.calls != 0 {
			t.Fatal("unsafe root staged")
		}
	}
}
