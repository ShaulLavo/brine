package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/ShaulLavo/brine/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := cli.NewRootCommand(cli.Dependencies{
		Context:  ctx,
		Stdin:    os.Stdin,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		Version:  cli.Version,
		LookPath: exec.LookPath,
		RunTUI:   cli.RunTUI,
	})
	if err := root.Execute(); err != nil {
		fmt.Fprintln(root.ErrOrStderr(), "error:", err)
		stop()
		os.Exit(1)
	}
}
