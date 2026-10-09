package caddy

import (
	"bytes"
	"context"
	"strconv"
	"testing"
)

func TestSnippetShadowRefusesBeforeStaging(t *testing.T) {
	for i, pattern := range []string{"%ROOT%/current/*.caddy", "%ROOT%/gen-1/*.caddy", "/other/file.caddy", "wild*", "imported"} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			m, root, main, state, v, r := setup(t)
			pattern = string(bytes.ReplaceAll([]byte(pattern), []byte("%ROOT%"), []byte(root)))
			main = append([]byte("("+pattern+") {\n}\n"), main...)
			if pattern == "imported" {
				main = append(main, []byte("import imported\n")...)
			}
			before := diskImage(t, root)
			if _, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t))); err == nil {
				t.Fatal("shadowing snippet accepted")
			}
			if !equalDisk(before, diskImage(t, root)) || len(v.paths) != 0 || r.calls != 0 {
				t.Fatal("shadow refusal staged or activated files")
			}
		})
	}
}

func TestOtherBrineReferencesRefuseBeforeStaging(t *testing.T) {
	for i, extra := range []string{"import %ROOT%/current/*\n", "import %ROOT%/current/hello.caddy\n", "import %ROOT%/gen-2/*.caddy\n", ":8081 {\n respond \"%ROOT%/current/*.caddy\"\n}\n"} {
		for _, installed := range []bool{false, true} {
			t.Run(strconv.Itoa(i)+strconv.FormatBool(installed), func(t *testing.T) {
				m, root, main, state, v, r := setup(t)
				if installed {
					first, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
					if err != nil {
						t.Fatal(err)
					}
					state = first.Next
				}
				main = append(main, bytes.ReplaceAll([]byte(extra), []byte("%ROOT%"), []byte(root))...)
				before := diskImage(t, root)
				calls := r.calls
				validations := len(v.paths)
				if _, err := m.Apply(context.Background(), main, state, Put(otherSite(t))); err == nil {
					t.Fatal("second reference to mutable Brine config accepted")
				}
				if !equalDisk(before, diskImage(t, root)) || len(v.paths) != validations || r.calls != calls {
					t.Fatal("reference refusal changed disk")
				}
			})
		}
	}
}

func TestBrineImportContinuationsRefuse(t *testing.T) {
	root := "/etc/caddy/brine"
	for _, suffix := range []string{"\\\n# operator comment\n", "\\\r\n# operator comment\n", "\\\n", "\\\r\n"} {
		main := []byte("import " + root + "/current/*.caddy" + suffix)
		if candidate, err := candidateRoot(main, root, "gen-2"); err == nil {
			t.Fatalf("continuation accepted and possibly changed %q", candidate)
		}
	}
	for _, prefix := range []string{"import \\\n", "# continued comment\\\nimport ", "import\t", "import \""} {
		path := root + "/current/*.caddy"
		end := "\n"
		if prefix == "import \"" {
			end = "\"\n"
		}
		if _, err := candidateRoot([]byte(prefix+path+end), root, "gen-2"); err == nil {
			t.Fatal("unusual Brine import accepted")
		}
	}
	original := []byte("# operator comment\r\n:8080 {\r\n respond import\r\n}\r\n  import " + root + "/current/*.caddy\r\n")
	got, err := candidateRoot(original, root, "gen-2")
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(original, []byte(root+"/current/*.caddy"), []byte(root+"/gen-2/*.caddy"), 1)
	if !bytes.Equal(got, want) {
		t.Fatal("candidate changed bytes outside the one path token")
	}
}

func TestSemanticMissingSiteBlocksPublication(t *testing.T) {
	m, root, main, state, v, r := setup(t)
	v.adapted = []byte(`{"apps":{"http":{"servers":{}}}}`)
	before := current(t, root)
	result, err := m.Apply(context.Background(), main, state, Put(fixtureSite(t)))
	if err == nil || result.Outcome != Unchanged {
		t.Fatal("missing adapted site accepted", result, err)
	}
	requireCurrent(t, root, before)
	if v.adaptCalls != 1 || r.calls != 0 {
		t.Fatal("adapt check missing or publication attempted")
	}
}
