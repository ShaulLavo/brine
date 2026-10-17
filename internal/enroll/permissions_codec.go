package enroll

import (
	"encoding/json"

	operatorpolicy "github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

// DecodePermissionsRequest closes every privileged request object before root entry.
func DecodePermissionsRequest(raw []byte) (PermissionsRequest, error) {
	var header map[string]json.RawMessage
	if err := json.Unmarshal(raw, &header); err != nil {
		return PermissionsRequest{}, err
	}
	action, err := strictjson.Value[string](header["action"])
	if err != nil {
		return PermissionsRequest{}, err
	}
	required := []string{"action", "identity_key", "host_key"}
	var optional []string
	switch action {
	case "show":
	case "plan":
		required = append(required, "changes")
		optional = []string{"app_label"}
	case "apply":
		required = append(required, "plan_id", "idempotency_key")
	case "status":
		required = append(required, "idempotency_key")
	default:
		return PermissionsRequest{}, strictjson.ErrObject
	}
	fields, err := permissionObject(raw, required, optional...)
	if err != nil {
		return PermissionsRequest{}, err
	}
	request := PermissionsRequest{Action: action}
	request.IdentityKey, err = strictjson.Value[[]byte](fields["identity_key"])
	if err != nil || len(request.IdentityKey) != 32 {
		return PermissionsRequest{}, strictjson.ErrObject
	}
	request.HostKey, err = strictjson.Value[string](fields["host_key"])
	if err != nil || request.HostKey == "" {
		return PermissionsRequest{}, strictjson.ErrObject
	}
	if err = decodePermissionRequestFields(fields, &request); err != nil {
		return PermissionsRequest{}, err
	}
	return request, validatePermissionsRequest(request)
}

func decodePermissionRequestFields(fields map[string]json.RawMessage, request *PermissionsRequest) error {
	var err error
	if raw, ok := fields["plan_id"]; ok {
		request.PlanID, err = strictjson.Value[string](raw)
		if err != nil {
			return err
		}
	}
	if raw, ok := fields["idempotency_key"]; ok {
		request.IdempotencyKey, err = strictjson.Value[string](raw)
		if err != nil {
			return err
		}
	}
	if raw, ok := fields["app_label"]; ok {
		request.AppLabel, err = strictjson.Value[string](raw)
		if err != nil {
			return err
		}
	}
	if raw, ok := fields["changes"]; ok {
		request.Changes, err = decodePermissionChanges(raw)
		if err != nil {
			return err
		}
	}
	return nil
}

func permissionObject(raw []byte, required []string, optional ...string) (map[string]json.RawMessage, error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, err
	}
	names := append([]string{}, required...)
	for _, key := range optional {
		if _, ok := keys[key]; ok {
			names = append(names, key)
		}
	}
	return strictjson.Object(raw, names...)
}

func decodePermissionStrings(raw []byte) ([]string, error) {
	values, err := strictjson.Value[[]json.RawMessage](raw)
	if err != nil {
		return nil, err
	}
	strings := make([]string, len(values))
	for i, value := range values {
		strings[i], err = strictjson.Value[string](value)
		if err != nil {
			return nil, err
		}
	}
	return strings, nil
}

func decodePermissionRegistry(raw []byte) (operatorpolicy.Registry, error) {
	fields, err := strictjson.Object(raw, "host", "repository_prefixes")
	if err != nil {
		return operatorpolicy.Registry{}, err
	}
	host, err := strictjson.Value[string](fields["host"])
	if err != nil {
		return operatorpolicy.Registry{}, err
	}
	prefixes, err := decodePermissionStrings(fields["repository_prefixes"])
	if err != nil {
		return operatorpolicy.Registry{}, err
	}
	return operatorpolicy.Registry{Host: host, RepositoryPrefixes: prefixes}, nil
}

func decodePermissionChange(raw []byte) (operatorpolicy.PermissionAction, error) {
	var header map[string]json.RawMessage
	if err := json.Unmarshal(raw, &header); err != nil {
		return operatorpolicy.PermissionAction{}, err
	}
	kind, err := strictjson.Value[operatorpolicy.PermissionKind](header["kind"])
	if err != nil {
		return operatorpolicy.PermissionAction{}, err
	}
	change := operatorpolicy.PermissionAction{Kind: kind}
	switch kind {
	case operatorpolicy.AllowDomain, operatorpolicy.RevokeDomain:
		fields, err := strictjson.Object(raw, "kind", "domain")
		if err != nil {
			return change, err
		}
		change.Domain, err = strictjson.Value[string](fields["domain"])
		if err != nil || change.Domain == "" {
			return change, strictjson.ErrObject
		}
	case operatorpolicy.AllowRegistry, operatorpolicy.RevokeRegistry:
		fields, err := strictjson.Object(raw, "kind", "registry")
		if err != nil {
			return change, err
		}
		registry, err := decodePermissionRegistry(fields["registry"])
		if err != nil || len(registry.RepositoryPrefixes) != 1 || registry.Host == "" || registry.RepositoryPrefixes[0] == "" {
			return change, strictjson.ErrObject
		}
		change.Registry = &registry
	default:
		return change, strictjson.ErrObject
	}
	return change, nil
}

func decodePermissionChanges(raw []byte) ([]operatorpolicy.PermissionAction, error) {
	values, err := strictjson.Value[[]json.RawMessage](raw)
	if err != nil || len(values) == 0 || len(values) > 128 {
		return nil, strictjson.ErrObject
	}
	changes := make([]operatorpolicy.PermissionAction, len(values))
	for i, value := range values {
		changes[i], err = decodePermissionChange(value)
		if err != nil {
			return nil, err
		}
	}
	return changes, nil
}

func decodePermissionView(raw []byte) (operatorpolicy.Permissions, error) {
	fields, err := strictjson.Object(raw, "registries", "domains")
	if err != nil {
		return operatorpolicy.Permissions{}, err
	}
	values, err := strictjson.Value[[]json.RawMessage](fields["registries"])
	if err != nil {
		return operatorpolicy.Permissions{}, err
	}
	registries := make([]operatorpolicy.Registry, len(values))
	for i, value := range values {
		registries[i], err = decodePermissionRegistry(value)
		if err != nil {
			return operatorpolicy.Permissions{}, err
		}
	}
	domains, err := decodePermissionStrings(fields["domains"])
	if err != nil {
		return operatorpolicy.Permissions{}, err
	}
	return operatorpolicy.Permissions{Registries: registries, Domains: domains}, nil
}

func decodePermissionPlan(raw []byte) (PermissionsPlan, error) {
	fields, err := permissionObject(raw, []string{"id", "enrollment_id", "before_hash", "after_hash", "changes", "before", "after", "no_op"}, "app_label")
	if err != nil {
		return PermissionsPlan{}, err
	}
	var p PermissionsPlan
	for key, destination := range map[string]*string{"id": &p.ID, "enrollment_id": &p.EnrollmentID, "before_hash": &p.BeforeHash, "after_hash": &p.AfterHash} {
		*destination, err = strictjson.Value[string](fields[key])
		if err != nil {
			return p, err
		}
	}
	if label, ok := fields["app_label"]; ok {
		p.AppLabel, err = strictjson.Value[string](label)
		if err != nil {
			return p, err
		}
	}
	p.Changes, err = decodePermissionChanges(fields["changes"])
	if err != nil {
		return p, err
	}
	p.Before, err = decodePermissionView(fields["before"])
	if err != nil {
		return p, err
	}
	p.After, err = decodePermissionView(fields["after"])
	if err != nil {
		return p, err
	}
	p.NoOp, err = strictjson.Value[bool](fields["no_op"])
	return p, err
}

func decodePermissionOperation(raw []byte) (PermissionsOperation, error) {
	fields, err := strictjson.Object(raw, "id", "plan_id", "state", "before_hash", "after_hash")
	if err != nil {
		return PermissionsOperation{}, err
	}
	var op PermissionsOperation
	for key, destination := range map[string]*string{"id": &op.ID, "plan_id": &op.PlanID, "state": &op.State, "before_hash": &op.BeforeHash, "after_hash": &op.AfterHash} {
		*destination, err = strictjson.Value[string](fields[key])
		if err != nil {
			return op, err
		}
	}
	return op, nil
}

func decodePermissionData(raw []byte, action string) (PermissionsResponse, error) {
	var response PermissionsResponse
	switch action {
	case "show":
		fields, err := strictjson.Object(raw, "policy_hash", "permissions")
		if err != nil {
			return response, err
		}
		response.PolicyHash, err = strictjson.Value[string](fields["policy_hash"])
		if err != nil {
			return response, err
		}
		view, err := decodePermissionView(fields["permissions"])
		response.Permissions = &view
		return response, err
	case "plan":
		fields, err := strictjson.Object(raw, "plan")
		if err != nil {
			return response, err
		}
		plan, err := decodePermissionPlan(fields["plan"])
		response.Plan = &plan
		return response, err
	case "apply", "status":
		fields, err := strictjson.Object(raw, "operation")
		if err != nil {
			return response, err
		}
		op, err := decodePermissionOperation(fields["operation"])
		response.Operation = &op
		return response, err
	}
	return response, strictjson.ErrObject
}
