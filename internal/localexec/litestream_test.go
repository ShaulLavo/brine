package localexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCaptureLitestreamAdmission(t *testing.T) {
	deadline, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cancelled, cancelNow := context.WithTimeout(context.Background(), time.Minute)
	cancelNow()
	for _, c := range []struct {
		name      string
		ctx       context.Context
		directory string
		kind      ErrorKind
	}{
		{"deadline required", context.Background(), t.TempDir(), Invalid},
		{"absolute directory required", deadline, "relative", Invalid},
		{"cancelled request", cancelled, t.TempDir(), Timeout},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := CaptureLitestream(c.ctx, c.directory, LitestreamCredentials{}, []string{"version"})
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != c.kind {
				t.Fatalf("got %v, want %s", err, c.kind)
			}
		})
	}
}

func TestLitestreamCredentialRedaction(t *testing.T) {
	credentials := LitestreamCredentials{AccessKey: "sensitive-access", SecretKey: "sensitive-secret", SessionToken: "sensitive-session"}
	encoded, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(encoded), fmt.Sprint(credentials), fmt.Sprintf("%#v", credentials)} {
		if strings.Contains(text, "sensitive") {
			t.Fatal("credentials escaped redaction")
		}
	}
}
