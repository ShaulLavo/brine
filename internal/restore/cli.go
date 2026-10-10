package restore

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Command struct {
	Args        []string
	Credentials Credentials
	Directory   string
}
type CommandResult struct {
	Stdout    []byte
	Truncated bool
}
type CLI interface {
	Execute(context.Context, Command) (CommandResult, error)
}
type ExecCLI struct{}

func (ExecCLI) Execute(ctx context.Context, c Command) (CommandResult, error) {
	if _, ok := ctx.Deadline(); !ok || !filepath.IsAbs(c.Directory) {
		return CommandResult{}, refuse("cli_deadline_required")
	}
	// Enrollment owns the executable hash pin. This boundary additionally refuses
	// a mutable install or PATH substitution before giving it environment secrets.
	for path := LitestreamPath; path != "/"; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return CommandResult{}, refuse("litestream_install_untrusted")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return CommandResult{}, refuse("litestream_install_untrusted")
		}
		if path == LitestreamPath && (!info.Mode().IsRegular() || info.Mode().Perm() != 0755) {
			return CommandResult{}, refuse("litestream_install_untrusted")
		}
	}
	cmd := exec.CommandContext(ctx, LitestreamPath, c.Args...)
	cmd.Dir = c.Directory
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=" + c.Directory, "TMPDIR=" + c.Directory, "AWS_EC2_METADATA_DISABLED=true", "AWS_ACCESS_KEY_ID=" + c.Credentials.AccessKey, "AWS_SECRET_ACCESS_KEY=" + c.Credentials.SecretKey}
	if c.Credentials.SessionToken != "" {
		cmd.Env = append(cmd.Env, "AWS_SESSION_TOKEN="+c.Credentials.SessionToken)
	}
	output := &boundedBuffer{}
	cmd.Stdout = output
	// Discard diagnostics. Upstream errors can contain credential-bearing URLs or
	// signing context, so they must never reach receipts or the domain error.
	cmd.Stderr = &boundedBuffer{}
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Run(); err != nil {
		return CommandResult{}, refuse("litestream_command_failed")
	}
	bytes, truncated := output.snapshot()
	return CommandResult{Stdout: bytes, Truncated: truncated}, nil
}

type boundedBuffer struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	size := len(data)
	remaining := (64 << 10) - len(b.data)
	if len(data) > remaining {
		data = data[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, data...)
	return size, nil
}
func (b *boundedBuffer) snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data, b.truncated
}
