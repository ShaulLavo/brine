//go:build linux

package host

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

const nativePacketPublisherIdentity = "c20038e4dd4b2f6c1514cb9db3e08f907897b39b5d8120db97254a87cfa676e5"

type nativePacketFixture struct {
	key     *ecdsa.PrivateKey
	private string
	public  string
	now     time.Time
	claims  map[string]any
}

func newNativePacketFixture(t *testing.T) nativePacketFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	return nativePacketFixture{key: key, private: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})), public: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public})), now: now, claims: map[string]any{"iss": "synthetic-issuer", "sub": "synthetic-subject", "hex_identity": nativePacketPublisherIdentity, "iat": now.Unix(), "aud": []string{"synthetic-native"}}}
}

func (f nativePacketFixture) token(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(f.claims)
	if err != nil {
		t.Fatal(err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(header + "." + payload))
	r, s, err := ecdsa.Sign(rand.Reader, f.key, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return header + "." + payload + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func (f nativePacketFixture) packet(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{"publisher": f.token(t), "signing_private": f.private, "signing_public": f.public, "auth_policy": map[string]any{"mode": "supplied_key_jwt", "require_client_token": true}, "publisher_expires_at": f.now.Add(time.Hour)}
}

func nativePacketJSON(t *testing.T, packet map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNativeMaterialPacketChecksSuppliedKeyAndExplicitDeliveryLease(t *testing.T) {
	f := newNativePacketFixture(t)
	packet := f.packet(t)
	material, expiry, err := decodeNativeMaterialPacket(nativePacketJSON(t, packet), f.now)
	if err != nil || !expiry.Equal(f.now.Add(time.Hour)) || material == nil {
		t.Fatal("valid supplied synthetic material did not decode", err)
	}
	defer material.Clear()
	if string(material.values[0]) != packet["publisher"] || string(material.values[1]) != f.private || string(material.values[2]) != f.public {
		t.Fatal("decoder changed supplied values")
	}
	if _, err := json.Marshal(material); err == nil {
		t.Fatal("private material became a public receipt")
	}
}

func TestNativeMaterialPacketRejectsKeySignatureClaimAndAuthDrift(t *testing.T) {
	for _, failure := range []string{"public", "private", "signature", "expiry", "signed-expiry", "missing-identity", "future-iat", "future-nbf", "auth-mode", "client-token", "unknown-role"} {
		checkNativePacketFailure(t, failure)
	}
}

func checkNativePacketFailure(t *testing.T, failure string) {
	t.Helper()
	f, other := newNativePacketFixture(t), newNativePacketFixture(t)
	packet := f.packet(t)
	switch failure {
	case "public":
		packet["signing_public"] = other.public
	case "private":
		packet["signing_private"] = other.private
	case "signature":
		packet["publisher"] = other.token(t)
	case "expiry":
		packet["publisher_expires_at"] = f.now
	case "signed-expiry":
		f.claims["exp"] = f.now.Add(time.Minute).Unix()
		packet["publisher"] = f.token(t)
	case "missing-identity":
		delete(f.claims, "hex_identity")
		packet["publisher"] = f.token(t)
	case "future-iat":
		f.claims["iat"] = f.now.Add(time.Hour).Unix()
		packet["publisher"] = f.token(t)
	case "future-nbf":
		f.claims["nbf"] = f.now.Add(time.Hour).Unix()
		packet["publisher"] = f.token(t)
	case "auth-mode":
		packet["auth_policy"] = map[string]any{"mode": "external_oidc", "require_client_token": true}
	case "client-token":
		packet["auth_policy"] = map[string]any{"mode": "supplied_key_jwt", "require_client_token": false}
	case "unknown-role":
		packet["binding_id"] = strings.Repeat("b", 32)
	}
	material, _, err := decodeNativeMaterialPacket(nativePacketJSON(t, packet), f.now)
	if err == nil || material != nil {
		t.Fatal("mismatched supplied material produced a delivery DTO", failure)
	}
}

func TestNativeMaterialPacketRejectsMissingNullDuplicateAndOversizedInput(t *testing.T) {
	f := newNativePacketFixture(t)
	raw := nativePacketJSON(t, f.packet(t))
	for _, invalid := range [][]byte{
		nil,
		[]byte(strings.Repeat("x", nativeMaterialPacketLimit+1)),
		[]byte(strings.Replace(string(raw), `"signing_public":`, `"signing_public":null,"signing_public":`, 1)),
		[]byte(strings.Replace(string(raw), `"signing_public":`, `"SigningPublic":`, 1)),
		[]byte(strings.Replace(string(raw), `"publisher_expires_at":`, `"publisher_expires_at":null,"publisher_expires_at":`, 1)),
	} {
		if material, _, err := decodeNativeMaterialPacket(invalid, f.now); err == nil || material != nil {
			t.Fatal("noncanonical packet shape produced private material")
		}
	}
}
