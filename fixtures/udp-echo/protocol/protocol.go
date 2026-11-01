// Package protocol defines the bounded test-only UDP nonce exchange.
package protocol

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
)

const (
	SchemaVersion      = 1
	FixtureName        = "brine-udp-echo"
	MaxDatagramBytes   = 1024
	MaxNonceBytes      = 256
	MaxReleaseBytes    = 64
	MaxVantageBytes    = 64
	MaxEndpointBytes   = 256
	MaxResponseBytes   = 768
	MaxHTTPBodyBytes   = 2048
	MaxHTTPHeaderBytes = 2048
	MaxAttempts        = 3
	MaxTimeoutSeconds  = 5
	MaxOverallSeconds  = 15
)

type Identity struct {
	Fixture string `json:"fixture"`
	Release string `json:"release"`
}

type Response struct {
	Schema  int    `json:"schema"`
	Fixture string `json:"fixture"`
	Release string `json:"release"`
	Nonce   string `json:"nonce"`
}

func ValidateIdentity(identity Identity) error {
	if identity.Fixture != FixtureName {
		return errors.New("unexpected fixture identity")
	}
	return ValidateToken("release", identity.Release, MaxReleaseBytes)
}

func ValidateNonce(nonce []byte) error {
	return ValidateToken("nonce", string(nonce), MaxNonceBytes)
}

func ValidateToken(name, value string, max int) error {
	if value == "" || len(value) > max {
		return fmt.Errorf("%s must contain 1-%d ASCII token bytes", name, max)
	}
	for _, c := range value {
		if !tokenRune(c) {
			return fmt.Errorf("%s must contain only ASCII letters, digits, dot, hyphen or underscore", name)
		}
	}
	return nil
}

func tokenRune(c rune) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_'
}

func NewNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func EncodeResponse(identity Identity, nonce []byte) ([]byte, error) {
	if err := ValidateIdentity(identity); err != nil {
		return nil, err
	}
	if err := ValidateNonce(nonce); err != nil {
		return nil, err
	}
	return json.Marshal(Response{Schema: SchemaVersion, Fixture: identity.Fixture, Release: identity.Release, Nonce: string(nonce)})
}

func DecodeResponse(body []byte) (Response, error) {
	if len(body) == 0 || len(body) > MaxResponseBytes {
		return Response{}, errors.New("response exceeds packet bound or is empty")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var response Response
	if err := decoder.Decode(&response); err != nil {
		return Response{}, errors.New("invalid response JSON")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Response{}, errors.New("response has trailing data")
	}
	if response.Schema != SchemaVersion {
		return Response{}, errors.New("response schema mismatch")
	}
	if err := ValidateIdentity(Identity{Fixture: response.Fixture, Release: response.Release}); err != nil {
		return Response{}, err
	}
	if err := ValidateNonce([]byte(response.Nonce)); err != nil {
		return Response{}, err
	}
	canonical, err := json.Marshal(response)
	if err != nil || !bytes.Equal(body, canonical) {
		return Response{}, errors.New("response must use the fixture canonical JSON")
	}
	return response, nil
}

func Endpoint(value string) (netip.AddrPort, error) {
	if len(value) > MaxEndpointBytes {
		return netip.AddrPort{}, errors.New("endpoint exceeds bound")
	}
	endpoint, err := netip.ParseAddrPort(value)
	if err != nil || !endpoint.IsValid() {
		return netip.AddrPort{}, errors.New("endpoint requires an IP literal and numeric port")
	}
	address := endpoint.Addr()
	if address.IsUnspecified() || address.IsMulticast() || address.Is4In6() || address.Zone() != "" || endpoint.Port() < 1024 {
		return netip.AddrPort{}, errors.New("endpoint requires a specified unzoned unicast address and unprivileged port")
	}
	return endpoint, nil
}
