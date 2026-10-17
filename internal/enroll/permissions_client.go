package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/transport"
)

type PermissionsClient struct{ Runner localexec.InputRunner }

const permissionsHostCommand = `if [ "$(id -u)" -eq 0 ]; then exec /usr/local/bin/brine host permissions --json; else exec sudo -n /usr/local/bin/brine host permissions --json; fi`

func (c PermissionsClient) Call(ctx context.Context, target transport.Target, configDir, admin string, request PermissionsRequest) (PermissionsResponse, error) {
	if err := target.Validate(); err != nil {
		return PermissionsResponse{}, err
	}
	if !adminDestination.MatchString(admin) {
		return PermissionsResponse{}, result.New(result.InvalidUsage, nil)
	}
	key, err := clientKey(filepath.Join(configDir, target.Name+".identity-key"), false)
	if err != nil {
		return PermissionsResponse{}, result.New(result.TransportInvalidTarget, err)
	}
	defer clear(key)
	pin, err := (transport.Client{KnownHostsDir: filepath.Join(configDir, "pins")}).PinnedHostFile(target)
	if err != nil {
		return PermissionsResponse{}, err
	}
	request.IdentityKey = key
	request.HostKey = target.PinnedHostKey
	raw, err := json.Marshal(request)
	if err != nil {
		return PermissionsResponse{}, err
	}
	defer clear(raw)
	args := adminArgs(admin)
	args = args[:len(args)-2]
	args = append(args, "-o", "BatchMode=yes", "-o", "UserKnownHostsFile="+pin, "-o", "GlobalKnownHostsFile=none", "-o", "HostKeyAlias=brine-pin", "-o", "HostKeyAlgorithms=ssh-ed25519", "-o", "VerifyHostKeyDNS=no", "-o", "UpdateHostKeys=no", "-o", "CheckHostIP=no", "--", admin, permissionsHostCommand)
	var environment []string
	if agent := os.Getenv("SSH_AUTH_SOCK"); agent != "" {
		environment = []string{"SSH_AUTH_SOCK=" + agent}
	}
	runner := c.Runner
	if runner == nil {
		runner = localexec.ExecRunner{}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, runErr := runner.RunInput(ctx, localexec.Command{Path: "ssh", Args: args, Stdin: raw, Env: environment, OutputLimit: dispatch.ResponseLimit})
	response, err := decodePermissionsResponse(out.Stdout, request)
	if err != nil {
		return PermissionsResponse{}, err
	}
	if runErr != nil {
		return PermissionsResponse{}, result.New(result.TransportFailure, errors.New("operator request outcome unknown; inspect permissions status with the same idempotency key"))
	}
	return response, nil
}

func decodePermissionsResponse(raw []byte, request PermissionsRequest) (PermissionsResponse, error) {
	invalid := func() (PermissionsResponse, error) {
		return PermissionsResponse{}, result.New(result.TransportInvalidResponse, nil)
	}
	if len(raw) > dispatch.ResponseLimit {
		return invalid()
	}
	fields, err := strictjson.Object(raw, "schema_version", "command", "ok", "data", "error")
	if err != nil {
		return invalid()
	}
	version, err := strictjson.Value[int](fields["schema_version"])
	if err != nil || version != result.SchemaVersion {
		return invalid()
	}
	command, err := strictjson.Value[string](fields["command"])
	if err != nil || command != "brine host permissions" {
		return invalid()
	}
	ok, err := strictjson.Value[bool](fields["ok"])
	if err != nil {
		return invalid()
	}
	if !ok {
		return permissionsResponseError(fields["error"])
	}
	if !bytes.Equal(bytes.TrimSpace(fields["error"]), []byte("null")) {
		return invalid()
	}
	var response PermissionsResponse
	decoder := json.NewDecoder(bytes.NewReader(fields["data"]))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&response); err != nil {
		return invalid()
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return invalid()
	}
	if !validPermissionResponse(response, request) {
		return invalid()
	}
	return response, nil
}

func permissionsResponseError(raw []byte) (PermissionsResponse, error) {
	var failure result.MachineError
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&failure); err != nil || !result.KnownCode(failure.Code) || failure.Details != nil {
		return PermissionsResponse{}, result.New(result.TransportInvalidResponse, nil)
	}
	domain := result.New(failure.Code, nil)
	expected := result.Failure("brine host permissions", domain).Error
	if failure.Message != expected.Message || failure.Retryable != expected.Retryable {
		return PermissionsResponse{}, result.New(result.TransportInvalidResponse, nil)
	}
	return PermissionsResponse{}, domain
}

func validPermissionResponse(response PermissionsResponse, request PermissionsRequest) bool {
	switch request.Action {
	case "show":
		return response.PolicyHash != "" && response.Permissions != nil && response.Plan == nil && response.Operation == nil
	case "plan":
		return response.Plan != nil && permissionID.MatchString(response.Plan.ID) && response.Operation == nil && response.Permissions == nil
	case "apply", "status":
		return response.Operation != nil && response.Operation.ID == request.IdempotencyKey && permissionID.MatchString(response.Operation.PlanID) && response.Plan == nil && response.Permissions == nil
	}
	return false
}
