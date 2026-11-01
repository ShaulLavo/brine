package enroll

import (
	"encoding/json"
	"maps"
	"slices"

	operatorpolicy "github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

func decodePermissionSecret(raw []byte) (operatorpolicy.SecretPermission, error) {
	fields, err := strictjson.Object(raw, "app", "reference")
	if err != nil {
		return operatorpolicy.SecretPermission{}, err
	}
	app, err := strictjson.Value[spec.Name](fields["app"])
	if err != nil {
		return operatorpolicy.SecretPermission{}, err
	}
	ref, err := strictjson.Value[spec.SecretReference](fields["reference"])
	secret := operatorpolicy.SecretPermission{App: app, Reference: ref}
	if err != nil || !secret.Valid() {
		return operatorpolicy.SecretPermission{}, strictjson.ErrObject
	}
	return secret, nil
}

func decodePermissionSecrets(raw []byte) (map[string][]string, error) {
	keys, err := strictjson.Value[map[string]json.RawMessage](raw)
	if err != nil {
		return nil, err
	}
	fields, err := strictjson.Object(raw, slices.Collect(maps.Keys(keys))...)
	if err != nil {
		return nil, err
	}
	secrets := make(map[string][]string, len(fields))
	for app, rawRefs := range fields {
		refs, err := decodePermissionSecretRefs(app, rawRefs)
		if err != nil {
			return nil, err
		}
		secrets[app] = refs
	}
	return secrets, nil
}

func decodePermissionSecretRefs(app string, raw []byte) ([]string, error) {
	refs, err := decodePermissionStrings(raw)
	if err != nil || !(operatorpolicy.SecretPermission{App: spec.Name(app), Reference: "probe"}).Valid() {
		return nil, strictjson.ErrObject
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		scope := operatorpolicy.SecretPermission{App: spec.Name(app), Reference: spec.SecretReference(ref)}
		if seen[ref] || !scope.Valid() {
			return nil, strictjson.ErrObject
		}
		seen[ref] = true
	}
	return refs, nil
}
