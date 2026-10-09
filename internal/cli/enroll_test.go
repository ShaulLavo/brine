package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestEnrollmentCannotAuthorizeItself(t *testing.T) {
	for _, args := range [][]string{{"enroll", "fixture", "--yes"}, {"enroll", "fixture", "--no-input"}, {"enroll", "fixture", "--json"}} {
		var out, diag bytes.Buffer
		err := Execute(Dependencies{Context: context.Background(), Stdin: strings.NewReader("fixture\n"), Stdout: &out, Stderr: &diag}, args)
		if err == nil {
			t.Fatalf("accepted nonterminal enrollment %v", args)
		}
		if strings.Contains(out.String(), "Enrolled") {
			t.Fatal("claimed enrollment")
		}
	}
}
