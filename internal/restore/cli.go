package restore

import (
	"context"
	"github.com/ShaulLavo/brine/internal/localexec"
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
	result, err := localexec.CaptureLitestream(ctx, c.Directory, localexec.LitestreamCredentials{AccessKey: c.Credentials.AccessKey, SecretKey: c.Credentials.SecretKey, SessionToken: c.Credentials.SessionToken}, c.Args)
	if err != nil {
		return CommandResult{}, refuse("litestream_command_failed")
	}
	return CommandResult{Stdout: []byte(result.Stdout), Truncated: result.Overflow}, nil
}
