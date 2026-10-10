package backupcredentials_test

import (
	"bytes"
	"context"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
)

func privateFixture(t *testing.T) backupcredentials.Packet {
	t.Helper()
	p, err := backupcredentials.DecodePacket([]byte(`{"access_key_id":"PLANTED_ACCESS","secret_access_key":"PLANTED_SECRET","session_token":"PLANTED_SESSION"}`))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExternalReflectionCannotReadPacketSecret(t *testing.T) {
	packet := privateFixture(t)
	// Exact reviewer path, guarded only because the fixed representation removes
	// the field entirely. Unexported strings remain readable with safe reflection.
	values := reflect.ValueOf(&packet).Elem().FieldByName("values")
	if values.IsValid() && values.Kind() == reflect.Pointer && !values.IsNil() {
		secret := values.Elem().FieldByName("secretKey")
		if secret.IsValid() && strings.Contains(secret.String(), "PLANTED") {
			t.Fatal("safe external reflection reached private secret")
		}
	}
}

func walkSafe(v reflect.Value, output *bytes.Buffer, depth int) {
	if !v.IsValid() || depth > 32 {
		return
	}
	switch v.Kind() {
	case reflect.String:
		output.WriteString(v.String())
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkSafe(v.Elem(), output, depth+1)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			walkSafe(v.Field(i), output, depth+1)
		}
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			walkSafe(v.Index(i), output, depth+1)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			walkSafe(iter.Key(), output, depth+1)
			walkSafe(iter.Value(), output, depth+1)
		}
		// Safe reflection cannot walk a function's captured variables.
	}
}

type recursiveHandler struct{ output *bytes.Buffer }

func (recursiveHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h recursiveHandler) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool { walkSafe(reflect.ValueOf(a.Value.Any()), h.output, 0); return true })
	return nil
}
func (h recursiveHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h recursiveHandler) WithGroup(string) slog.Handler      { return h }

func TestRecursiveSafeReflectionAndSlogCannotReadPacket(t *testing.T) {
	packet := privateFixture(t)
	var output bytes.Buffer
	walkSafe(reflect.ValueOf(&packet), &output, 0)
	slog.New(recursiveHandler{&output}).Info("private", "packet", packet, "pointer", &packet)
	if strings.Contains(output.String(), "PLANTED") {
		t.Fatal("recursive safe reflection logged credential values")
	}
	if len(packet.Environment()) != 3 {
		t.Fatal("explicit environment accessor lost credentials")
	}
	copyPacket := packet
	packet.Clear()
	for _, value := range copyPacket.Environment() {
		if strings.Contains(value, "PLANTED") {
			t.Fatal("clear retained captured credentials")
		}
	}
}
