package policy

import (
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type backupDocument struct {
	MinSyncInterval     *string `toml:"min_sync_interval" json:"min_sync_interval"`
	MaxSyncInterval     *string `toml:"max_sync_interval" json:"max_sync_interval"`
	SnapshotInterval    *string `toml:"snapshot_interval" json:"snapshot_interval"`
	MinSnapshotInterval *string `toml:"min_snapshot_interval" json:"min_snapshot_interval"`
	MaxSnapshotInterval *string `toml:"max_snapshot_interval" json:"max_snapshot_interval"`
}

func normalizeBackup(raw *backupDocument) (data.BackupCadence, error) {
	out := data.DefaultBackupCadence()
	if raw == nil {
		return out, nil
	}
	for _, f := range []struct {
		text     **string
		duration *time.Duration
	}{
		{&raw.MinSyncInterval, &out.MinSyncInterval}, {&raw.MaxSyncInterval, &out.MaxSyncInterval},
		{&raw.SnapshotInterval, &out.SnapshotInterval}, {&raw.MinSnapshotInterval, &out.MinSnapshotInterval}, {&raw.MaxSnapshotInterval, &out.MaxSnapshotInterval},
	} {
		if *f.text != nil {
			d, err := time.ParseDuration(**f.text)
			if err != nil || d <= 0 {
				return out, refuse("policy.invalid_backup_cadence", "backup", "backup cadence requires positive Go durations")
			}
			*f.duration = d
		}
		value := f.duration.String()
		*f.text = &value
	}
	if out.MinSyncInterval < 10*time.Second || out.MaxSyncInterval > time.Hour || out.MinSnapshotInterval < time.Hour || out.MaxSnapshotInterval > 24*time.Hour || out.MinSyncInterval > out.MaxSyncInterval || out.MinSnapshotInterval > out.MaxSnapshotInterval || out.SnapshotInterval < out.MinSnapshotInterval || out.SnapshotInterval > out.MaxSnapshotInterval {
		return out, refuse("policy.invalid_backup_cadence", "backup", "backup cadence is outside the admitted bounds")
	}
	return out, nil
}
func (p Policy) Backup() data.BackupCadence {
	if p.config == nil {
		return data.BackupCadence{}
	}
	raw := p.config.Backup
	if raw != nil {
		copy := *raw
		cadence, _ := normalizeBackup(&copy)
		return cadence
	}
	return data.DefaultBackupCadence()
}
