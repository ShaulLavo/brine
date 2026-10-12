package spacetimedb

import (
	"errors"
	"strings"
	"testing"
)

func TestNativePreflightDescriptionIsOwnedSyntaxOnly(t *testing.T) {
	token := "0x" + strings.Repeat("a", 64)
	raw := []byte(`{"AutoMigrate":{"migrate_plan":"actual native plan","break_clients":false,"token":"` + token + `","major_version_upgrade":false}}`)
	parsed, err := DescribeNativePreflight(raw)
	if err != nil || !parsed.Automatic() || parsed.MajorUpgrade() || parsed.value().breaks || string(parsed.TokenBytes()) != token || parsed.ResponseSHA256() != publicationBytesSHA256(raw) || parsed.TokenSHA256() != publicationBytesSHA256([]byte(token)) {
		t.Fatal("closed native response description refused", err)
	}
	responseSHA := parsed.ResponseSHA256()
	parsed.TokenBytes()[0] = 'b'
	raw[0] = 'x'
	if string(parsed.TokenBytes()) != token || parsed.ResponseSHA256() != responseSHA {
		t.Fatal("caller changed retained decoded token")
	}
	if _, err = marshalOpaqueNative(parsed); err == nil {
		t.Fatal("private token description leaked through JSON", err)
	}
	if _, err = DescribeNativePreflight(raw); !errors.Is(err, ErrNativePreflight) {
		t.Fatal("malformed native syntax admitted", err)
	}
}
