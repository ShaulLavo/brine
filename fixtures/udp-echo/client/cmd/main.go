package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ShaulLavo/brine/fixtures/udp-echo/client"
	"github.com/ShaulLavo/brine/fixtures/udp-echo/protocol"
)

func main() {
	endpoint := flag.String("endpoint", "", "UDP host:port")
	nonce := flag.String("nonce", "", "bounded nonce; generated when omitted")
	release := flag.String("release", "", "expected release identity")
	attempts := flag.Int("attempts", 3, "maximum read-only attempts")
	timeout := flag.Duration("attempt-timeout", time.Second, "per-attempt deadline")
	overall := flag.Duration("overall-timeout", 5*time.Second, "overall probe deadline")
	vantage := flag.String("vantage", "external-client", "network vantage label")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "positional arguments are not accepted; use -endpoint")
		os.Exit(2)
	}
	if *nonce == "" {
		var err error
		*nonce, err = protocol.NewNonce()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	result, err := client.Probe(context.Background(), client.Config{Endpoint: *endpoint, Nonce: *nonce, ExpectedRelease: *release, Attempts: *attempts, AttemptTimeout: *timeout, OverallTimeout: *overall, Vantage: *vantage})
	if err != nil && result.Outcome == "configuration_error" {
		result.Error = err.Error()
	}
	body, marshalErr := client.MarshalResult(result)
	if marshalErr != nil {
		fmt.Fprintln(os.Stderr, marshalErr)
		os.Exit(2)
	}
	_, _ = os.Stdout.Write(body)
	if result.Outcome != "ok" {
		os.Exit(1)
	}
}
