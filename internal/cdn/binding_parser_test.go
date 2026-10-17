package cdn

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestBindingParserClosesEveryNestedObject(t *testing.T) {
	canonical, err := fixtureBinding().CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, from, to string }{
		{"root duplicate", `"reference":"public"`, `"reference":"public","reference":"public"`},
		{"root alias", `"reference":"public"`, `"Reference":"public"`},
		{"root missing", `"reference":"public",`, ``},
		{"origin duplicate", `"host":"game.example.test"`, `"host":"game.example.test","host":"game.example.test"`},
		{"origin alias", `"tls_name":`, `"TLSName":`},
		{"origin unknown", `"origin":{`, `"origin":{"credential":"do-not-echo",`},
		{"origin null", `"origin":{`, `"origin":null,"extra":{`},
		{"cache duplicate", `"prefix":"/objects/"`, `"prefix":"/objects/","prefix":"/objects/"`},
		{"cache alias", `"ttl_seconds":`, `"TTLSeconds":`},
		{"cache missing", `"prefix":"/objects/",`, ``},
		{"cache null", `"extensions":["ogg","png"]`, `"extensions":null`},
		{"edge duplicate", `"allowed_edges":[{`, `"allowed_edges":[{"reference":"edge-a",`},
		{"edge alias", `"allowed_edges":[{"reference":`, `"allowed_edges":[{"Reference":`},
		{"edge null", `"allowed_edges":[{`, `"allowed_edges":[null,{`},
		{"traffic duplicate", `"traffic":{`, `"traffic":{"reference":"edge-a",`},
		{"traffic alias", `"traffic":{"reference":`, `"traffic":{"Reference":`},
		{"traffic unknown", `"traffic":{`, `"traffic":{"credential":"do-not-echo",`},
		{"authority duplicate", `"resource_id":"existing-record"`, `"resource_id":"existing-record","resource_id":"existing-record"`},
		{"authority alias", `"resource_id":`, `"ResourceID":`},
		{"authority unknown", `"traffic_authority":{`, `"traffic_authority":{"credential":"do-not-echo",`},
		{"authority null", `"traffic_authority":{`, `"traffic_authority":null,"extra":{`},
	} {
		t.Run(test.name, func(t *testing.T) { rejectBindingMutation(t, canonical, test.from, test.to) })
	}
	if _, err := ParseBinding([]byte(strings.Repeat(" ", 64<<10) + string(canonical))); err == nil {
		t.Fatal("oversized binding accepted")
	}
}

func rejectBindingMutation(t *testing.T, valid []byte, from, to string) {
	t.Helper()
	mutated := bytes.Replace(valid, []byte(from), []byte(to), 1)
	if bytes.Equal(mutated, valid) {
		t.Fatal("test did not change binding")
	}
	if _, err := ParseBinding(mutated); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected fixed binding refusal, got %v", err)
	}
}
