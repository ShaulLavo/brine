package data

// InitializationBounds are explicit operator limits. A zero value never admits
// an initialization; there is no guessed backup age or recovery-window default.
type InitializationBounds struct {
	MaxBackupAgeSeconds      uint32 `json:"max_backup_age_seconds" toml:"max_backup_age_seconds"`
	MaxRestoreTestAgeSeconds uint32 `json:"max_restore_test_age_seconds" toml:"max_restore_test_age_seconds"`
	RecoveryWindowSeconds    uint32 `json:"recovery_window_seconds" toml:"recovery_window_seconds"`
}

func (b InitializationBounds) Valid() bool {
	return b.MaxBackupAgeSeconds > 0 && b.MaxBackupAgeSeconds <= 86400 && b.MaxRestoreTestAgeSeconds > 0 && b.MaxRestoreTestAgeSeconds <= 300 && b.RecoveryWindowSeconds > 0 && b.RecoveryWindowSeconds <= 30*86400
}
