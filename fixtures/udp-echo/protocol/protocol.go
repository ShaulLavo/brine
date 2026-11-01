package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	SchemaVersion     = 1
	FixtureName       = "brine-udp-echo"
	MaxDatagramBytes  = 1024
	MaxNonceBytes     = 256
	MaxReleaseBytes   = 64
	MaxVantageBytes   = 64
	MaxEndpointBytes  = 256
	MaxResponseBytes  = 768
	MaxJSONBodyBytes  = 2048
	MaxAttempts       = 3
	MaxTimeoutSeconds = 5
)

type Identity struct {
	Fixture string
	Release string
}

type Response struct {
	Schema  int    `json:"schema"`
	Fixture string `json:"fixture"`
	Release string `json:"release"`
	Nonce   string `json:"nonce"`
}

func ValidateIdentity(identity Identity) error {
	if identity.Fixture != FixtureName {
		return fmt.Errorf("fixture must be %q", FixtureName)
	}
	if err := validateToken("release", identity.Release, MaxReleaseBytes); err != nil {
		return err
	}
	return nil
}

func ValidateNonce(nonce []byte) error {
	if len(nonce) == 0 {
		return errors.New("nonce is empty")
	}
	if len(nonce) > MaxNonceBytes {
		return fmt.Errorf("nonce exceeds %d bytes", MaxNonceBytes)
	}
	if !utf8.Valid(nonce) {
		return errors.New("nonce is not UTF-8")
	}
	for _, r := range string(nonce) {
		if r < 0x20 || r == 0x7f {
			return errors.New("nonce contains a control character")
		}
	}
	return nil
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
	body, err := json.Marshal(Response{Schema: SchemaVersion, Fixture: identity.Fixture, Release: identity.Release, Nonce: string(nonce)})
	if err != nil {
		return nil, fmt.Errorf("encode response: %w", err)
	}
	if len(body) > MaxResponseBytes || len(body) > MaxDatagramBytes {
		return nil, errors.New("encoded response exceeds packet bound")
	}
	return body, nil
}

func DecodeResponse(body []byte) (Response, error) {
	if len(body) == 0 || len(body) > MaxResponseBytes || len(body) > MaxDatagramBytes {
		return Response{}, errors.New("response exceeds packet bound")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var response Response
	if err := decoder.Decode(&response); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Response{}, errors.New("response has trailing JSON")
		}
		return Response{}, fmt.Errorf("response trailing data: %w", err)
	}
	if response.Schema != SchemaVersion || response.Fixture != FixtureName {
		return Response{}, errors.New("response identity mismatch")
	}
	if err := ValidateIdentity(Identity{Fixture: response.Fixture, Release: response.Release}); err != nil {
		return Response{}, err
	}
	if err := ValidateNonce([]byte(response.Nonce)); err != nil {
		return Response{}, err
	}
	return response, nil
}

func ValidateEndpoint(endpoint string) error {
	return validateToken("endpoint", endpoint, MaxEndpointBytes)
}

func ValidateVantage(vantage string) error {
	return validateToken("vantage", vantage, MaxVantageBytes)
}

func validateToken(name, value string, max int) error {
	if value == "" {
		return fmt.Errorf("%s is empty", name)
	}
	if len(value) > max {
		return fmt.Errorf("%s exceeds %d bytes", name, max)
	}
	if !utf8.ValidString(value) || strings.IndexAny(value, "\r\n\x00") >= 0 {
		return fmt.Errorf("%s contains invalid characters", name)
	}
	return nil
}
