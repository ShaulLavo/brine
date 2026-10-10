//go:build !linux

package cli

import "github.com/ShaulLavo/brine/internal/replication"

func replaceReplicaProcess(string, []string, []string) error { return replication.ErrPermit }
