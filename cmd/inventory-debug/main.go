//go:build inventorydebug

// A test-only host entry point. The operator supplies the stable HMAC key on stdin.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/target"
)

func main() {
	key, e := io.ReadAll(io.LimitReader(os.Stdin, 65))
	if e != nil || len(key) < 32 || len(key) > 64 {
		fmt.Fprintln(os.Stderr, "inventory requires a 32-64 byte identity key on stdin")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s, e := (inventory.Collector{FS: inventory.HostFS{}, Runner: localexec.ExecRunner{}, IdentityKey: key}).Collect(ctx)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	data, e := target.Encode(s)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	decoded, e := target.Decode(data)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if e = decoded.Validate(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if _, e = os.Stdout.Write(append(data, '\n')); e != nil {
		os.Exit(1)
	}
}
