package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
)

type PermissionsClient struct{ Runner localexec.InputRunner }

const permissionsHostCommand = `if [ "$(id -u)" -eq 0 ]; then exec /usr/local/bin/brine host permissions; else exec sudo -n /usr/local/bin/brine host permissions; fi`

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
	var envelope result.Envelope
	if err = json.Unmarshal(out.Stdout, &envelope); err != nil {
		return PermissionsResponse{}, result.New(result.TransportFailure, errors.New("operator request outcome unknown; inspect permissions status with the same idempotency key"))
	}
	if !envelope.OK {
		return PermissionsResponse{}, result.New(envelope.Error.Code, nil)
	}
	if runErr != nil {
		return PermissionsResponse{}, result.New(result.TransportFailure, nil)
	}
	encoded, err := json.Marshal(envelope.Data)
	if err != nil {
		return PermissionsResponse{}, err
	}
	var response PermissionsResponse
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&response); err != nil {
		return PermissionsResponse{}, result.New(result.TransportInvalidResponse, err)
	}
	return response, nil
}
