package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/ShaulLavo/brine/integration/udp-delivery/brine-drill"
)

func main() {
	manifestPath := flag.String("manifest", "", "absolute manifest path")
	ledgerPath := flag.String("ledger", "", "absolute local ledger path")
	phaseName := flag.String("phase", "", "before-reboot, after-reboot, cleanup, inspect or resume")
	dryRun := flag.Bool("dry-run", false, "validate and print commands without writing or invoking")
	flag.Parse()
	if *manifestPath == "" || *ledgerPath == "" || *phaseName == "" {
		fatal("manifest, ledger and phase are required")
	}
	manifest, err := brinedrill.LoadManifest(*manifestPath)
	if err != nil {
		fatal(err.Error())
	}
	phase := brinedrill.Phase(*phaseName)
	ledger := brinedrill.FileLedger{Path: *ledgerPath, RunID: manifest.RunID, ManifestHash: manifestHash(manifest), Resources: manifest.OwnedResources}
	runner := brinedrill.Runner{Manifest: manifest, Ledger: ledger, DryRun: *dryRun}
	report, err := runner.RunPhase(context.Background(), phase)
	if encodeErr := json.NewEncoder(os.Stdout).Encode(report); encodeErr != nil {
		fatal(encodeErr.Error())
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func manifestHash(manifest brinedrill.Manifest) string {
	return brinedrill.ManifestHashForCLI(manifest)
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(2)
}
