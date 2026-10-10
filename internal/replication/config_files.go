package replication

import (
	"context"
	"time"
)

type ConfigFiles interface {
	ReadConfig(context.Context, string) ([]byte, error)
}

// ConfigCadence extracts the immutable committed schedule. Callers must first
// verify the recorded content hash and then ParseConfig against its binding.
func ConfigCadence(raw []byte) (Cadence, error) {
	config, err := decodeConfig(raw)
	if err != nil || len(config.DBs) != 1 {
		return Cadence{}, ErrInvalid
	}
	syncInterval, syncErr := time.ParseDuration(config.DBs[0].Replica.SyncInterval)
	snapshotInterval, snapshotErr := time.ParseDuration(config.Snapshot.Interval)
	cadence := Cadence{SyncInterval: syncInterval, SnapshotInterval: snapshotInterval}
	if syncErr != nil || snapshotErr != nil || cadence.Validate() != nil {
		return Cadence{}, ErrInvalid
	}
	return cadence, nil
}
