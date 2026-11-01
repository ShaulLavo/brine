package protocol

import (
	"strings"
	"testing"
)

func TestValidateNonceRejectsInvalidInputs(t *testing.T) {
	cases := []struct {
		name  string
		nonce []byte
	}{
		{name: "empty"},
		{name: "too long", nonce: []byte(strings.Repeat("a", MaxNonceBytes+1))},
		{name: "control", nonce: []byte("nonce\n")},
		{name: "invalid utf8", nonce: []byte{0xff}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateNonce(test.nonce); err == nil {
				t.Fatal("invalid nonce accepted")
			}
		})
	}
}

func TestResponseRoundTripRejectsDriftAndTrailingData(t *testing.T) {
	identity := Identity{Fixture: FixtureName, Release: "release-a"}
	encoded, err := EncodeResponse(identity, []byte("nonce-a"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := DecodeResponse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if response.Release != identity.Release || response.Nonce != "nonce-a" {
		t.Fatalf("unexpected response: %+v", response)
	}
	for _, input := range [][]byte{
		append(append([]byte(nil), encoded...), []byte("{}")...),
		[]byte(`{"schema":1,"fixture":"other","release":"release-a","nonce":"nonce-a"}`),
		[]byte(strings.Repeat("x", MaxResponseBytes+1)),
	} {
		if _, err := DecodeResponse(input); err == nil {
			t.Fatalf("invalid response accepted: %q", input)
		}
	}
}

func TestNewNonceIsBoundedHex(t *testing.T) {
	nonce, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	if len(nonce) != 32 {
		t.Fatalf("nonce length: %d", len(nonce))
	}
	if err := ValidateNonce([]byte(nonce)); err != nil {
		t.Fatal(err)
	}
}

func TestMaximumTokensFitResponse(t *testing.T) {
	encoded, err := EncodeResponse(Identity{Fixture: FixtureName, Release: strings.Repeat("r", MaxReleaseBytes)}, []byte(strings.Repeat("n", MaxNonceBytes)))
	if err != nil || len(encoded) > MaxResponseBytes {
		t.Fatalf("maximum valid tokens exceed response bound: %d %v", len(encoded), err)
	}
	if _, err := DecodeResponse(encoded); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointRejectsNameResolutionAndInvalidAddresses(t *testing.T) {
	for _, value := range []string{"localhost:7777", "127.0.0.1:80", "127.0.0.1:0", "0.0.0.0:7777", "[::]:7777", "[fe80::1%lo]:7777", "224.0.0.1:7777", "[::ffff:127.0.0.1]:7777"} {
		if _, err := Endpoint(value); err == nil {
			t.Fatalf("invalid endpoint accepted: %s", value)
		}
	}
	for _, value := range []string{"127.0.0.1:7777", "[::1]:7777"} {
		if _, err := Endpoint(value); err != nil {
			t.Fatalf("valid endpoint refused: %s %v", value, err)
		}
	}
}
