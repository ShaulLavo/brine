package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type observeInput struct {
	Host                string   `json:"host"`
	Port                uint16   `json:"port"`
	DatabaseIdentity    string   `json:"database_identity"`
	PublisherIdentity   string   `json:"publisher_identity"`
	Names               []string `json:"names"`
	PublisherCredential string   `json:"publisher_credential"`
}

func main() {
	mode := flag.String("mode", "canonical", "canonical or observe; all input is bounded stdin JSON")
	flag.Parse()
	if flag.NArg() != 0 {
		fail()
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		fail()
	}
	var output any
	switch *mode {
	case "canonical":
		output, err = canonical(raw)
	case "observe":
		output, err = observe(raw)
	default:
		fail()
	}
	if err != nil {
		fail()
	}
	if json.NewEncoder(os.Stdout).Encode(output) != nil {
		fail()
	}
}

func fail() {
	fmt.Fprintln(os.Stderr, "SpacetimeDB runtime helper refused input or observation")
	os.Exit(1)
}

func canonical(raw []byte) (any, error) {
	schema, err := spacetimedb.CanonicalSchemaV10(raw, spacetimedb.DefaultSchemaLimits())
	if err != nil {
		return nil, err
	}
	return map[string]any{"canonicalizer": schema.Version(), "schema_sha256": schema.SHA256(), "schema_marker": schema.Marker(), "canonical": json.RawMessage(schema.Bytes())}, nil
}

func observe(raw []byte) (any, error) {
	fields, err := strictjson.Object(raw, "host", "port", "database_identity", "publisher_identity", "names", "publisher_credential")
	if err != nil {
		return nil, err
	}
	input, err := strictjson.Value[observeInput](raw)
	if err != nil {
		return nil, err
	}
	if _, err := strictjson.Value[[]string](fields["names"]); err != nil {
		return nil, err
	}
	endpoint, err := spacetimedb.NewPrivateEndpoint(input.Host, input.Port, input.DatabaseIdentity)
	if err != nil {
		return nil, err
	}
	credential, err := spacetimedb.NewPublisherCredential([]byte(input.PublisherCredential))
	if err != nil {
		return nil, err
	}
	expected, err := spacetimedb.NewExpectedDatabase(input.DatabaseIdentity, input.PublisherIdentity, input.Names)
	if err != nil {
		return nil, err
	}
	current, err := spacetimedb.ObserveCurrent(context.Background(), endpoint, credential, expected, spacetimedb.DefaultObserverLimits())
	if err != nil {
		return nil, err
	}
	return map[string]any{"database_identity": current.Database(), "publisher_identity": current.Publisher(), "names": current.Names(),
		"host_type": current.HostType(), "runtime_module_hash": current.CurrentModule(), "canonicalizer": current.Schema().Version(),
		"schema_sha256": current.Schema().SHA256(), "schema_marker": current.Schema().Marker()}, nil
}
