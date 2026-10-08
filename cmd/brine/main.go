package main

import (
	"os"

	"github.com/ShaulLavo/brine/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
