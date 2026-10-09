package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/transport"
	"github.com/spf13/cobra"
)

type LogsClient interface {
	Call(context.Context, transport.Target, dispatch.Request) (result.Envelope, error)
}

var targetName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func newLogsCmd(deps Dependencies, modes *machineModes) *cobra.Command {
	var name, configDir, since string
	tail := 100
	cmd := &cobra.Command{Use: "logs APP", Short: "Read a bounded, best-effort redacted app log tail", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		request := logs.Request{App: args[0], Tail: tail, Since: since}
		if err := request.Validate(); err != nil {
			return err
		}
		if !targetName.MatchString(name) {
			return result.New(result.InvalidUsage, nil)
		}
		if configDir == "" {
			dir, err := os.UserConfigDir()
			if err != nil {
				return result.New(result.TransportInvalidTarget, nil)
			}
			configDir = filepath.Join(dir, "brine", "targets")
		}
		target, err := transport.LoadTarget(filepath.Join(configDir, name+".json"))
		if err != nil {
			return err
		}
		if target.Name != name {
			return result.New(result.TransportInvalidTarget, nil)
		}
		client := deps.LogsClient
		if client == nil {
			client = transport.Client{KnownHostsDir: filepath.Join(configDir, "pins")}
		}
		raw, err := json.Marshal(request)
		if err != nil {
			return result.New(result.InternalError, nil)
		}
		response, err := client.Call(cmd.Context(), target, dispatch.Request{SchemaVersion: dispatch.SchemaVersion, Op: "logs", RequestID: "logs", Args: raw})
		if err != nil {
			return err
		}
		if !response.OK {
			return result.New(result.TransportInvalidResponse, nil)
		}
		raw, err = json.Marshal(response.Data)
		if err != nil {
			return result.New(result.TransportInvalidResponse, nil)
		}
		lines, err := logs.DecodeLines(raw)
		if err != nil {
			return err
		}
		if len(lines) > tail {
			return result.New(result.TransportInvalidResponse, nil)
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		if modes.jsonl {
			for _, line := range lines {
				if err := encoder.Encode(result.Success(cmd.CommandPath(), logEvent{Event: "log", Line: line})); err != nil {
					return err
				}
			}
			return encoder.Encode(result.Success(cmd.CommandPath(), logComplete{Event: "complete", Count: len(lines)}))
		}
		if modes.json {
			return encoder.Encode(result.Success(cmd.CommandPath(), lines))
		}
		for _, line := range lines {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s [%d] %s\n", line.Timestamp, line.Priority, strings.ReplaceAll(line.Message, "\n", `\n`)); err != nil {
				return err
			}
		}
		return nil
	}}
	cmd.Flags().StringVar(&name, "target", "", "Enrolled target name (required)")
	cmd.Flags().IntVar(&tail, "tail", 100, "Maximum log entries (1-1000)")
	cmd.Flags().StringVar(&since, "since", "", "Earliest timestamp (RFC3339)")
	cmd.Flags().StringVar(&configDir, "config-dir", "", "Private client target directory")
	return cmd
}

type logEvent struct {
	Event string    `json:"event"`
	Line  logs.Line `json:"line"`
}
type logComplete struct {
	Event string `json:"event"`
	Count int    `json:"count"`
}
