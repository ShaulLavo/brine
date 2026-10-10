package quadlet

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/ShaulLavo/brine/internal/replication"
)

type ReplicaUnitOptions struct {
	Binding                                  replication.Binding
	Config                                   []byte
	ConfigPath, CredentialPath, LifetimeLock string
}

var credentialLayout = regexp.MustCompile(`^s3/[a-z][a-z0-9-]{0,62}/v[1-9][0-9]*\.env$`)

// RenderReplica produces an ordinary user service, never a container Quadlet.
// replica-exec repeats the read-only permit after flock and execs with a fresh
// environment. Credential values never become command-line arguments.
func RenderReplica(o ReplicaUnitOptions) (Unit, error) {
	if _, err := replication.ParseConfig(o.Config, o.Binding); err != nil {
		return Unit{}, err
	}
	suffix := "/replication/" + o.Binding.BindingID + "/configs/" + strings.TrimPrefix(replication.ConfigHash(o.Config), "sha256:") + ".yml"
	if !strings.HasSuffix(o.ConfigPath, suffix) {
		return Unit{}, replication.ErrInvalid
	}
	state := strings.TrimSuffix(o.ConfigPath, suffix)
	if !unitPath(state) || o.LifetimeLock != state+"/replica-locks/"+o.Binding.BindingID+".lock" || o.Binding.SocketPath != state+"/replication/"+o.Binding.BindingID+"/control.sock" || !strings.HasPrefix(o.CredentialPath, state+"/credentials/") || !credentialLayout.MatchString(strings.TrimPrefix(o.CredentialPath, state+"/credentials/")) {
		return Unit{}, replication.ErrInvalid
	}
	name, err := replication.ServiceName(o.Binding.BindingID)
	if err != nil {
		return Unit{}, err
	}
	hash := replication.ConfigHash(o.Config)
	args := strings.Join([]string{o.Binding.DatabaseID, o.Binding.BindingID, o.Binding.EpochID, hash}, " ")
	var b strings.Builder
	fmt.Fprintf(&b, "# Brine-owned replica=%s config=%s\n\n[Unit]\nDescription=Brine database replication %s\n\n[Service]\nType=simple\nStandardOutput=null\nStandardError=null\nUMask=0077\nEnvironmentFile=%s\nExecStartPre=/usr/local/bin/brine host replica-permit %s\nExecStart=/usr/local/bin/brine host replica-exec %s %s %s\nRestart=on-failure\nRestartSec=5s\nTimeoutStopSec=60s\nKillMode=control-group\n\n[Install]\nWantedBy=default.target\n", o.Binding.BindingID, hash, o.Binding.DatabaseID, o.CredentialPath, args, args, o.ConfigPath, o.CredentialPath)
	return Unit{name: name, content: b.String()}, nil
}
func unitPath(s string) bool {
	return path.IsAbs(s) && s != "/" && path.Clean(s) == s && !strings.ContainsAny(s, "\x00\n\r\t %$\\\"'")
}
