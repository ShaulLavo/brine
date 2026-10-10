package replication

import (
	"context"
	"strings"
)

type LaunchReader interface {
	PermitReader
	// The implementation securely reads the immutable file and rejects mode,
	// ownership, link count, symlink ancestry and credential grammar drift.
	ReadCredentialEnvironment(context.Context, string) ([]string, error)
}
type ReplicaExecRequest struct {
	ReplicaPermitRequest
	ConfigPath, CredentialPath string
}
type ReplaceProcess func(string, []string, []string) error

func ReplicaExec(ctx context.Context, r LaunchReader, req ReplicaExecRequest, replace ReplaceProcess) error {
	if r == nil || replace == nil || ctx.Err() != nil || !idPattern.MatchString(req.DatabaseID) || !idPattern.MatchString(req.BindingID) || !idPattern.MatchString(req.EpochID) || !hashPattern.MatchString(req.ConfigHash) || !safePath(req.ConfigPath) || !safePath(req.CredentialPath) {
		return ErrPermit
	}
	initial, err := r.ReadReplicaPermit(ctx, req.BindingID)
	if err != nil || !matchesRequest(initial, req.ReplicaPermitRequest) || initial.ConfigPath != req.ConfigPath || initial.CredentialPath != req.CredentialPath || !safePath(req.ConfigPath) || !safePath(req.CredentialPath) {
		return ErrPermit
	}
	lock, err := AcquireLifetimeLock(ctx, initial.LifetimeLock)
	if err != nil {
		return err
	}
	defer lock.Release()
	current, err := r.ReadReplicaPermit(ctx, req.BindingID)
	if err != nil || current.ConfigPath != req.ConfigPath || current.CredentialPath != req.CredentialPath || current.LifetimeLock != initial.LifetimeLock || !matchesRequest(current, req.ReplicaPermitRequest) {
		return ErrPermit
	}
	credentials, err := r.ReadCredentialEnvironment(ctx, req.CredentialPath)
	if err != nil {
		return ErrPermit
	}
	env, err := executionEnvironment(credentials)
	if err != nil || ctx.Err() != nil {
		return ErrPermit
	}
	if err = lock.inherit(); err != nil {
		return ErrPermit
	}
	return replace(Executable, []string{Executable, "replicate", "-config", req.ConfigPath}, env)
}
func executionEnvironment(credentials []string) ([]string, error) {
	allowed := map[string]bool{"AWS_ACCESS_KEY_ID": true, "AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true}
	seen := map[string]bool{}
	env := []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=/nonexistent", "AWS_EC2_METADATA_DISABLED=true", "AWS_CONFIG_FILE=/dev/null", "AWS_SHARED_CREDENTIALS_FILE=/dev/null"}
	for _, entry := range credentials {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !allowed[key] || seen[key] || value == "" {
			return nil, ErrPermit
		}
		for _, r := range value {
			if r < 33 || r > 126 || strings.ContainsRune("\"'\\$`", r) {
				return nil, ErrPermit
			}
		}
		seen[key] = true
		env = append(env, entry)
	}
	if !seen["AWS_ACCESS_KEY_ID"] || !seen["AWS_SECRET_ACCESS_KEY"] {
		return nil, ErrPermit
	}
	return env, nil
}
