package policy

import (
	"slices"

	"github.com/ShaulLavo/brine/internal/spec"
)

type SecretPermission struct {
	App       spec.Name            `json:"app"`
	Reference spec.SecretReference `json:"reference"`
}

func (s SecretPermission) Valid() bool {
	probe := probeApp()
	probe["name"] = string(s.App)
	if _, err := parseMap(probe); err != nil {
		return false
	}
	stem := "brine." + string(s.App) + "." + string(s.Reference)
	return referencePattern.MatchString(string(s.Reference)) && len(stem+".v18446744073709551615") <= 253
}

func cloneAllowedSecrets(input map[string][]string) map[string][]string {
	output := make(map[string][]string, len(input))
	for app, references := range input {
		output[app] = slices.Clone(references)
	}
	return output
}

func editSecretPermission(next *document, action PermissionAction) string {
	app, ref := string(action.Secret.App), string(action.Secret.Reference)
	key := "secret:" + app + ":" + ref
	values := next.AllowedSecrets[app]
	index := slices.Index(values, ref)
	if action.Kind == AllowSecret && index >= 0 || action.Kind == RevokeSecret && index < 0 {
		return key
	}
	if next.AllowedSecrets == nil {
		next.AllowedSecrets = map[string][]string{}
	}
	if action.Kind == AllowSecret {
		next.AllowedSecrets[app] = append(values, ref)
		return key
	}
	values = slices.Delete(values, index, index+1)
	if len(values) == 0 {
		delete(next.AllowedSecrets, app)
		return key
	}
	next.AllowedSecrets[app] = values
	return key
}
