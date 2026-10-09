package strictjson

import (
	"encoding/json"
	"testing"
)

func TestExactObjectFields(t *testing.T) {
	for _, data := range []string{`[]`, `null`, `{}`, `{"Name":"x"}`, `{"name":"x","extra":1}`, `{"name":"x","name":"y"}`, `{"name":"x","name":"y"}`, `{"name":"x"} {}`, `{"name":"x"} trailing`, "{\"name\":\"\xff\"}"} {
		if _, err := Object([]byte(data), "name"); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	fields, err := Object([]byte(`{"name":"x"}`), "name")
	if err != nil {
		t.Fatal(err)
	}
	name, err := Value[string](fields["name"])
	if err != nil || name != "x" {
		t.Fatal("value")
	}
	if _, err := Value[string](json.RawMessage(`null`)); err == nil {
		t.Fatal("null scalar")
	}
	if _, err := Value[string](json.RawMessage(`1`)); err == nil {
		t.Fatal("number string")
	}
	if _, err := Object([]byte(` { } `)); err != nil {
		t.Fatal(err)
	}
}
