package localexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const LitestreamPath = "/opt/brine/litestream/0.5.17/litestream"

// LitestreamCredentials are explicit destination credentials, never an ambient profile.
type LitestreamCredentials struct {
	AccessKey    string `json:"-"`
	SecretKey    string `json:"-"`
	SessionToken string `json:"-"`
}

func (LitestreamCredentials) String() string   { return "[redacted credentials]" }
func (LitestreamCredentials) GoString() string { return "[redacted credentials]" }

// CaptureLitestream runs only the protected pinned executable. The caller supplies
// a deadline, typed argv and a fresh private working directory. Enrollment owns
// the executable's hash pin; this boundary refuses mutable installs before giving
// them credentials. Diagnostics are bounded and discarded, never returned.
func CaptureLitestream(ctx context.Context, directory string, credentials LitestreamCredentials, args []string) (Capture, error) {
	if _, ok := ctx.Deadline(); !ok || !filepath.IsAbs(directory) {
		return Capture{}, &Error{Kind: Invalid}
	}
	if ctx.Err() != nil {
		return Capture{}, &Error{Kind: Timeout}
	}
	for path := LitestreamPath; path != "/"; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return Capture{}, &Error{Kind: Invalid}
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return Capture{}, &Error{Kind: Invalid}
		}
		if path == LitestreamPath && (!info.Mode().IsRegular() || info.Mode().Perm() != 0755) {
			return Capture{}, &Error{Kind: Invalid}
		}
	}
	// #nosec G204 -- Fixed protected executable; arguments are separate argv, never a shell command.
	cmd := exec.CommandContext(ctx, LitestreamPath, args...)
	cmd.Dir = directory
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=" + directory, "TMPDIR=" + directory, "AWS_EC2_METADATA_DISABLED=true", "AWS_ACCESS_KEY_ID=" + credentials.AccessKey, "AWS_SECRET_ACCESS_KEY=" + credentials.SecretKey}
	if credentials.SessionToken != "" {
		cmd.Env = append(cmd.Env, "AWS_SESSION_TOKEN="+credentials.SessionToken)
	}
	output := &boundedOutput{limit: 64 << 10}
	cmd.Stdout = output
	cmd.Stderr = &boundedOutput{limit: 64 << 10}
	configureProcessGroup(cmd)
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Run(); err != nil {
		return Capture{}, &Error{Kind: Failed}
	}
	return output.snapshot(), nil
}
