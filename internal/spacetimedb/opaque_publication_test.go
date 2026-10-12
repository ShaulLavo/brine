package spacetimedb

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestOpaqueNativePublicationValuesHidePayloadAndHandleNil(t *testing.T) {
	secret := "0x" + strings.Repeat("a", 64)
	description, err := DescribeNativePreflight([]byte(`{"AutoMigrate":{"migrate_plan":"actual native plan","break_clients":false,"token":"` + secret + `","major_version_upgrade":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{description, newNativePublicationPreflight(&nativePublicationPreflight{token: secret}), NativeCreateReceipt(nil), NativePublicationReceipt(nil), NativePreflightDescription(nil)} {
		if _, err = json.Marshal(value); err == nil {
			t.Fatal("opaque native value acquired JSON representation")
		}
		if strings.Contains(fmt.Sprintf("%v %+v %#v", value, value, value), secret) {
			t.Fatal("opaque native token leaked through formatting")
		}
	}
	var preflight *NativePublicationPreflight
	if raw, err := marshalOpaqueNative(preflight); err != nil || string(raw) != "null" {
		t.Fatal("nil native pointer supplied public fields", err)
	}
	if preflight.Automatic() || preflight.MajorUpgrade() || preflight.BreaksClients() || len(preflight.ResponseBytes()) != 0 || len(preflight.TokenBytes()) != 0 {
		t.Fatal("nil preflight supplied native migration facts")
	}
	if (NativeCreateReceipt(nil)).BindingSHA256() != "" || (NativePublicationReceipt(nil)).BindingSHA256() != "" || (NativePreflightDescription(nil)).Automatic() {
		t.Fatal("nil opaque value manufactured native provenance")
	}
}

func marshalOpaqueNative(value any) ([]byte, error) { return json.Marshal(value) }
