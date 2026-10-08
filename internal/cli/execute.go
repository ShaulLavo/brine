package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

// Execute owns presentation for one invocation, including parser failures.
// Machine output is buffered until execution finishes so failures cannot follow
// a partial success response. An unavailable stdout still returns a write error.
func Execute(deps Dependencies, args []string) error {
	machine := requestsJSON(args)
	stdout := deps.Stdout
	var output bytes.Buffer
	if machine {
		deps.Stdout = &output
	}
	root := NewRootCommand(deps)
	root.SetArgs(append([]string{}, args...))
	command, _, findErr := root.Find(args)
	name := "brine"
	if findErr == nil && command != nil {
		name = command.CommandPath()
	}
	var err error
	if deps.Context.Err() != nil {
		err = deps.Context.Err()
	} else if findErr != nil {
		err = result.New(result.InvalidUsage, findErr)
	} else {
		_, err = root.ExecuteC()
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

// Inspect the mode before Cobra parsing, which can stop at an earlier bad flag.
// Values after -- are positional, and an explicit false retains human output.
func requestsJSON(args []string) bool {
	enabled := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == "--json" {
			enabled = true
		} else if strings.HasPrefix(arg, "--json=") {
			value, err := strconv.ParseBool(strings.TrimPrefix(arg, "--json="))
			enabled = value || err != nil
		}
	}
	return enabled
}

func usageError(_ *cobra.Command, err error) error { return result.New(result.InvalidUsage, err) }
