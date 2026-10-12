package datapublish

import (
	"strings"
	"testing"
)

func TestPreservedOriginalCannotResumePublicationOrForgeCursorScope(t *testing.T) {
	plan, err := NewPlan(planFixture(t, true))
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := NewCursor(plan, "apply-module", strings.Repeat("a", 64), strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	cursor, err = cursor.Intend(RestoreStage)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err = cursor.MarkUnknown()
	if err != nil {
		t.Fatal(err)
	}
	preserved, err := cursor.PreserveSource(strings.Repeat("c", 64))
	if err != nil || preserved.State() != SourcePreserved {
		t.Fatal(err)
	}
	if _, ready := preserved.NextStep(); ready {
		t.Fatal("source-preserved cursor offered another native effect")
	}
	if _, err = preserved.Intend(RestoreStage); err == nil {
		t.Fatal("source preservation rewound consumed attempt")
	}
	if _, err = preserved.PreserveSource(strings.Repeat("d", 64)); err == nil {
		t.Fatal("source preservation could be rewritten")
	}
	raw, err := preserved.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCursor(raw, plan, recordSHA256(raw))
	if err != nil || decoded.State() != SourcePreserved {
		t.Fatal("actual preserved cursor failed replay", err)
	}
	changed := []byte(strings.Replace(string(raw), `"apply-module"`, `"other-module"`, 1))
	if _, err = DecodeCursor(changed, plan, recordSHA256(raw)); err == nil {
		t.Fatal("new operation reused retained source preservation")
	}
}
