package enroll

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ShaulLavo/brine/internal/localexec"
)

func TestInstalledPackageStateBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, output, want string
		exit               int
		refuse             bool
	}{
		{name: "actual missing uidmap", output: "not-installed\t"},
		{name: "missing package record", exit: 1},
		{name: "retained removed metadata", output: "not-installed\t1:4.17.4-2"},
		{name: "removed configuration", output: "config-files\t1:4.17.4-2"},
		{name: "installed uidmap", output: "installed\t1:4.17.4-2", want: "1:4.17.4-2"},
		{name: "missing separator", output: "not-installed", refuse: true},
		{name: "missing installed version", output: "installed\t", refuse: true},
		{name: "missing removed version", output: "config-files\t", refuse: true},
		{name: "extra column", output: "installed\t1.0\textra", refuse: true},
		{name: "extra record", output: "not-installed\t\ninstalled\t1.0", refuse: true},
		{name: "invalid version", output: "not-installed\t;changed", refuse: true},
		{name: "unknown state", output: "unknown\t1.0", refuse: true},
		{name: "half installed", output: "half-installed\t1.0", refuse: true},
		{name: "unpacked", output: "unpacked\t1.0", refuse: true},
		{name: "half configured", output: "half-configured\t1.0", refuse: true},
		{name: "triggers pending", output: "triggers-pending\t1.0", refuse: true},
		{name: "triggers awaited", output: "triggers-awaited\t1.0", refuse: true},
		{name: "query failure", exit: 2, refuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeAdmin{handle: func(c localexec.Command) (localexec.Result, error) {
				if c.Path != "dpkg-query" || c.Mutation || !slices.Equal(c.Args, []string{"-W", "-f=${db:Status-Status}\t${Version}", "uidmap"}) {
					t.Fatalf("package state query changed: %+v", c)
				}
				if tc.exit != 0 {
					return localexec.Result{Stdout: tc.output}, &localexec.Error{ExitCode: tc.exit}
				}
				return localexec.Result{Stdout: tc.output}, nil
			}}
			got, err := installed(context.Background(), probeRunner{r}, "uidmap")
			if got != tc.want || (err != nil) != tc.refuse {
				t.Fatalf("installed output=%q version=%q error=%v", tc.output, got, err)
			}
		})
	}
}

func TestUnknownPackageProbeFailureIsNotAbsence(t *testing.T) {
	r := &fakeAdmin{handle: func(localexec.Command) (localexec.Result, error) {
		return localexec.Result{}, errors.New("query did not complete")
	}}
	if _, err := installed(context.Background(), probeRunner{r}, "uidmap"); err == nil {
		t.Fatal("unknown package query accepted as missing")
	}
}
