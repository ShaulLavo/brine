package policy

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestMinimumFreeDiskPolicy(t *testing.T) {
	for _, value := range []string{"", "1073741824", "2147483648", "1", "9223372036854775807"} {
		t.Run(value, func(t *testing.T) {
			raw := fixture(t)
			if value != "" {
				raw = append([]byte("minimum_free_disk_bytes = "+value+"\n"), raw...)
			}
			p, err := Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			desired, err := Normalize(app(t), p)
			if err != nil {
				t.Fatal(err)
			}
			data, err := desired.CanonicalBytes()
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			want := value
			if want == "" {
				want = "1073741824"
			}
			if string(fields["minimum_free_disk_bytes"]) != want {
				t.Fatalf("minimum not bound to desired config: %s", data)
			}
			canonical, err := p.CanonicalBytes()
			if err != nil || !bytes.Contains(canonical, []byte(`"minimum_free_disk_bytes":`+want)) {
				t.Fatal("minimum not bound to policy", err)
			}
		})
	}
	defaultPolicy, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := Parse(append([]byte("minimum_free_disk_bytes = 1073741824\n"), fixture(t)...))
	if err != nil || explicit.Hash() != defaultPolicy.Hash() {
		t.Fatal("explicit default changed policy", err)
	}
	changed, err := Parse(append([]byte("minimum_free_disk_bytes = 2147483648\n"), fixture(t)...))
	if err != nil || changed.Hash() == defaultPolicy.Hash() {
		t.Fatal("policy minimum not hashed", err)
	}
}

func TestInvalidMinimumFreeDiskPolicy(t *testing.T) {
	for _, value := range []string{"0", "-1", "1.5", `"1073741824"`, "9223372036854775808", "[]", "{}"} {
		t.Run(value, func(t *testing.T) {
			if _, err := Parse(append([]byte("minimum_free_disk_bytes = "+value+"\n"), fixture(t)...)); err == nil {
				t.Fatal("invalid minimum accepted")
			}
		})
	}
}
