package cli

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

// Execute owns presentation for one invocation, including parser failures.
// Machine output is buffered until execution finishes so failures cannot follow
// a partial success response. An unavailable stdout still returns a write error.
func Execute(deps Dependencies, args []string) error {
	modes := requestedModes(args)
	machine := modes.enabled()
	stdout := deps.Stdout
	var output bytes.Buffer
	if machine {
		deps.Stdout = &output
	}
	root := NewRootCommand(deps)
	root.SetArgs(append([]string{}, args...))

	name := "brine"
	var command *cobra.Command
	var err error
	if modes.json && modes.jsonl {
		command, _, _ = root.Find(args)
		err = result.New(result.InvalidUsage, nil)
	} else {
		command, err = root.ExecuteC()
	}
	if command != nil {
		name = command.CommandPath()
	}
	if err != nil {
		if _, _, findErr := root.Find(args); findErr != nil {
			err = result.New(result.InvalidUsage, err)
		} else if command != nil && command.Name() == cobra.ShellCompRequestCmd {
			// Cobra adds this command during execution; its only validator is MinimumNArgs.
			err = result.New(result.InvalidUsage, err)
		}
	}
	if deps.Context.Err() != nil {
		err = deps.Context.Err()
	}
	if err != nil {
		err = result.Classify(err)
	}
	if machine {
		var writeErr error
		if err != nil {
			writeErr = json.NewEncoder(stdout).Encode(result.Failure(name, err))
		} else if json.Valid(output.Bytes()) {
			_, writeErr = stdout.Write(output.Bytes())
		} else {
			writeErr = json.NewEncoder(stdout).Encode(result.Success(name, map[string]string{"help": output.String()}))
		}
		if writeErr != nil {
			err = result.New(result.InternalError, writeErr)
		}
	}
	if err != nil {
		fmt.Fprintln(deps.Stderr, "error:", err)
	}
	return err
}

func usageError(_ *cobra.Command, err error) error { return result.New(result.InvalidUsage, err) }
