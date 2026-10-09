package transport

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/result"
)

const CallTimeout = 15 * time.Second

type Client struct {
	KnownHostsDir string
	Runner        localexec.InputRunner
	LookPath      func(string) (string, error)
}

// Call never retries. A timeout does not establish a remote operation's outcome.
func (c Client) Call(ctx context.Context, t Target, request dispatch.Request) (result.Envelope, error) {
	return c.call(ctx, t, request, "brine host serve")
}

// VerifyRestriction must still return the dispatcher response to a command that
// could not invoke it on an unrestricted shell key.
func (c Client) VerifyRestriction(ctx context.Context, t Target, request dispatch.Request) (result.Envelope, error) {
	return c.call(ctx, t, request, "printf brine-unrestricted-key")
}

func (c Client) call(ctx context.Context, t Target, request dispatch.Request, remoteCommand string) (result.Envelope, error) {
	if err := t.Validate(); err != nil {
		return result.Envelope{}, err
	}
	input, err := dispatch.EncodeRequest(request)
	if err != nil {
		return result.Envelope{}, err
	}
	path, err := c.pin(t)
	if err != nil {
		return result.Envelope{}, err
	}
	lookup := c.LookPath
	if lookup == nil {
		lookup = localexec.LookPath
	}
	ssh, err := lookup("ssh")
	if err != nil {
		return result.Envelope{}, result.New(result.DependencyMissing, err)
	}
	args := []string{"-T"}
	for _, option := range []string{
		"BatchMode=yes", "StrictHostKeyChecking=yes", "UserKnownHostsFile=" + path,
		"GlobalKnownHostsFile=none", "HostKeyAlgorithms=ssh-ed25519", "ForwardAgent=no", "ClearAllForwardings=yes",
		"IdentitiesOnly=yes", "RequestTTY=no", "PermitLocalCommand=no", "PreferredAuthentications=publickey",
		"VerifyHostKeyDNS=no", "UpdateHostKeys=no",
		"CheckHostIP=no", "HostKeyAlias=brine-pin", "ConnectTimeout=5", "ServerAliveInterval=5", "ServerAliveCountMax=1",
	} {
		args = append(args, "-o", option)
	}
	args = append(args, "-i", t.IdentityPath, "--", t.Destination, remoteCommand)
	runner := c.Runner
	if runner == nil {
		runner = localexec.ExecRunner{}
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	var env []string
	if agent := os.Getenv("SSH_AUTH_SOCK"); agent != "" {
		env = []string{"SSH_AUTH_SOCK=" + agent}
	}
	output, runErr := runner.RunInput(ctx, localexec.Command{Path: ssh, Args: args, Stdin: input, Env: env, OutputLimit: dispatch.ResponseLimit})
	if ctx.Err() != nil {
		return result.Envelope{}, result.New(result.TransportFailure, ctx.Err())
	}
	// SSH status 255 describes transport failure, not a dispatcher exit category.
	if runErr != nil {
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) || exit.ExitCode() == 255 {
			return result.Envelope{}, result.New(result.TransportFailure, runErr)
		}
	}
	response, err := dispatch.DecodeResponse(output.Stdout, request.Op)
	if err != nil {
		return result.Envelope{}, err
	}
	if !response.OK {
		domain := result.New(response.Error.Code, nil)
		var exit *exec.ExitError
		if !errors.As(runErr, &exit) || exit.ExitCode() != result.ExitCode(domain) {
			return result.Envelope{}, result.New(result.TransportInvalidResponse, runErr)
		}
		return response, domain
	}
	if runErr != nil {
		return result.Envelope{}, result.New(result.TransportInvalidResponse, runErr)
	}
	return response, nil
}

func (c Client) pin(t Target) (string, error) {
	fail := func(err error) (string, error) { return "", result.New(result.TransportInvalidTarget, err) }
	if !safePath(c.KnownHostsDir) {
		return fail(nil)
	}
	if err := os.MkdirAll(c.KnownHostsDir, 0700); err != nil {
		return fail(err)
	}
	info, err := os.Lstat(c.KnownHostsDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return fail(err)
	}
	path := filepath.Join(c.KnownHostsDir, t.Name+".known_hosts")
	want := []byte("brine-pin " + t.PinnedHostKey + "\n")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return fail(err)
		}
		existingFile, err := os.Open(path)
		if err != nil {
			return fail(err)
		}
		existing, readErr := io.ReadAll(io.LimitReader(existingFile, int64(len(want)+1)))
		err = errors.Join(readErr, existingFile.Close())
		if err != nil || string(existing) != string(want) {
			return fail(err)
		}
		return path, nil
	}
	if err != nil {
		return fail(err)
	}
	_, writeErr := file.Write(want)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fail(err)
	}
	return path, nil
}
