package main

import (
	"os"

	"github.com/ShaulLavo/deployctl/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
