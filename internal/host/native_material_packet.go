//go:build linux

package host

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

const nativeMaterialPacketLimit = 32 << 10

var errNativeMaterialPacket = errors.New("native material packet refused")

func decodeNativeMaterialPacket(raw []byte, now time.Time) (*EngineSecretMaterial, time.Time, error) {
	if len(raw) == 0 || len(raw) > nativeMaterialPacketLimit {
		return nil, time.Time{}, errNativeMaterialPacket
	}
	fields, err := strictjson.Object(raw, "publisher", "signing_private", "signing_public", "auth_policy", "publisher_expires_at")
	if err != nil {
		return nil, time.Time{}, errNativeMaterialPacket
	}
	publisher, tokenErr := strictjson.Value[string](fields["publisher"])
	private, privateErr := strictjson.Value[string](fields["signing_private"])
	public, publicErr := strictjson.Value[string](fields["signing_public"])
	expiry, expiryErr := strictjson.Value[time.Time](fields["publisher_expires_at"])
	if tokenErr != nil || privateErr != nil || publicErr != nil || expiryErr != nil || expiry.Location() != time.UTC || !expiry.After(now) || !engineAuthPolicy(fields["auth_policy"]) {
		return nil, time.Time{}, errNativeMaterialPacket
	}
	key, err := nativeMaterialKeyPair([]byte(private), []byte(public))
	if err != nil || nativeMaterialToken(publisher, key, now, expiry) != nil {
		return nil, time.Time{}, errNativeMaterialPacket
	}
	material, err := NewEngineSecretMaterial([]byte(publisher), []byte(private), []byte(public), fields["auth_policy"])
	if err != nil {
		return nil, time.Time{}, errNativeMaterialPacket
	}
	return material, expiry, nil
}

func nativeMaterialKeyPair(private, public []byte) (*ecdsa.PublicKey, error) {
	secret, trailing := pem.Decode(private)
	if secret == nil || secret.Type != "PRIVATE KEY" || len(strings.TrimSpace(string(trailing))) != 0 {
		return nil, errNativeMaterialPacket
	}
	parsed, err := x509.ParsePKCS8PrivateKey(secret.Bytes)
	clear(secret.Bytes)
	key, ok := parsed.(*ecdsa.PrivateKey)
	if err != nil || !ok || key.Curve != elliptic.P256() {
		return nil, errNativeMaterialPacket
	}
	defer key.D.SetInt64(0)
	visible, trailing := pem.Decode(public)
	if visible == nil || visible.Type != "PUBLIC KEY" || len(strings.TrimSpace(string(trailing))) != 0 {
		return nil, errNativeMaterialPacket
	}
	parsed, err = x509.ParsePKIXPublicKey(visible.Bytes)
	publicKey, ok := parsed.(*ecdsa.PublicKey)
	if err != nil || !ok || publicKey.Curve != elliptic.P256() || !publicKey.Equal(&key.PublicKey) {
		return nil, errNativeMaterialPacket
	}
	return publicKey, nil
}

func nativeMaterialToken(token string, key *ecdsa.PublicKey, now, expiry time.Time) error {
	if len(token) == 0 || len(token) > 16<<10 {
		return errNativeMaterialPacket
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errNativeMaterialPacket
	}
	header, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || len(header) > 1024 {
		return errNativeMaterialPacket
	}
	fields, err := strictjson.Object(header, "alg", "typ")
	if err != nil || string(fields["alg"]) != `"ES256"` || string(fields["typ"]) != `"JWT"` {
		return errNativeMaterialPacket
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != 64 {
		return errNativeMaterialPacket
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(key, hash[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return errNativeMaterialPacket
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(claims) > 16<<10 {
		return errNativeMaterialPacket
	}
	defer clear(claims)
	return nativeMaterialClaims(claims, now, expiry)
}

func nativeMaterialClaims(raw []byte, now, expiry time.Time) error {
	var names map[string]json.RawMessage
	if json.Unmarshal(raw, &names) != nil || names == nil || len(names) > 8 {
		return errNativeMaterialPacket
	}
	fields := make([]string, 0, len(names))
	for name := range names {
		if name != "iss" && name != "sub" && name != "aud" && name != "iat" && name != "exp" && name != "nbf" && name != "hex_identity" {
			return errNativeMaterialPacket
		}
		fields = append(fields, name)
	}
	claims, err := strictjson.Object(raw, fields...)
	if err != nil {
		return errNativeMaterialPacket
	}
	issuer, issuerErr := strictjson.Value[string](claims["iss"])
	subject, subjectErr := strictjson.Value[string](claims["sub"])
	identity, identityErr := strictjson.Value[string](claims["hex_identity"])
	issued, issuedErr := strictjson.Value[int64](claims["iat"])
	if issuerErr != nil || subjectErr != nil || identityErr != nil || issuedErr != nil || issuer == "" || subject == "" || !data.ValidCatalogHash(identity) || issued > now.Unix() {
		return errNativeMaterialPacket
	}
	if raw, present := claims["aud"]; present {
		audience, err := strictjson.Value[[]string](raw)
		if err != nil || len(audience) > 16 {
			return errNativeMaterialPacket
		}
	}
	if raw, present := claims["exp"]; present {
		seconds, err := strictjson.Value[int64](raw)
		if err != nil || seconds <= now.Unix() || expiry.After(time.Unix(seconds, 0)) {
			return errNativeMaterialPacket
		}
	}
	if raw, present := claims["nbf"]; present {
		seconds, err := strictjson.Value[int64](raw)
		if err != nil || seconds > now.Unix() {
			return errNativeMaterialPacket
		}
	}
	return nil
}
