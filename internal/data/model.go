// Package data owns persistent database identities and replica ownership records.
package data

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

type AppIncarnationID string
type DatabaseID string
type ReplicaBindingID string
type ReplicaEpochID string
type ArchiveID string
type FenceID string

type DatabaseName string
type PersistentRoot string
type ContainerMountPath string
type DatabaseFilename string
type BackupDestinationRef string

type RuntimeIdentity struct {
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}
type Database struct {
	Name              DatabaseName         `json:"name"`
	PersistentRoot    PersistentRoot       `json:"persistent_root"`
	MountPath         ContainerMountPath   `json:"mount_path"`
	Filename          DatabaseFilename     `json:"filename"`
	BackupDestination BackupDestinationRef `json:"backup_destination"`
}
type SchemaCompatibility struct {
	Database DatabaseName `json:"database"`
	Accepts  []string     `json:"accepts"`
	Startup  string       `json:"startup"`
}
type SchemaDefinition struct {
	Database      DatabaseName `json:"database"`
	Marker        string       `json:"marker"`
	CatalogSHA256 string       `json:"catalog_sha256"`
}
type DatabaseBinding struct {
	DatabaseID        DatabaseID         `json:"database_id"`
	Name              DatabaseName       `json:"name"`
	IncarnationID     AppIncarnationID   `json:"incarnation_id"`
	Root              PersistentRoot     `json:"root"`
	RelativeDirectory string             `json:"relative_directory"`
	MountPath         ContainerMountPath `json:"mount_path"`
	Filename          DatabaseFilename   `json:"filename"`
	ReplicaBindingID  ReplicaBindingID   `json:"replica_binding_id"`
}

// Destination has references only. Credential values never enter the store.
type Destination struct {
	Reference     BackupDestinationRef `json:"reference"`
	Endpoint      string               `json:"endpoint"`
	Region        string               `json:"region"`
	Bucket        string               `json:"bucket"`
	BasePrefix    string               `json:"base_prefix"`
	PathStyle     bool                 `json:"path_style"`
	CredentialRef string               `json:"credential_ref"`
}
type ReplicaBinding struct {
	BindingID         ReplicaBindingID `json:"binding_id"`
	DatabaseID        DatabaseID       `json:"database_id"`
	Destination       Destination      `json:"destination"`
	EpochID           ReplicaEpochID   `json:"epoch_id"`
	RemotePrefix      string           `json:"remote_prefix"`
	CredentialVersion uint64           `json:"credential_version"`
	CredentialFile    string           `json:"credential_file"`
	UnitSHA256        string           `json:"unit_sha256"`
	ConfigSHA256      string           `json:"config_sha256"`
	ConfigContent     string           `json:"config_content"`
	ConfigFile        string           `json:"config_file"`
	SocketFile        string           `json:"socket_file"`
	LifetimeLockFile  string           `json:"lifetime_lock_file"`
	Committed         bool             `json:"committed"`
}
type FenceState string

const (
	FenceHeld     FenceState = "held"
	FenceReleased FenceState = "released"
)

type QuiescenceFence struct {
	ID            FenceID          `json:"id"`
	IncarnationID AppIncarnationID `json:"incarnation_id"`
	DatabaseID    DatabaseID       `json:"database_id"`
	OperationID   string           `json:"operation_id"`
	State         FenceState       `json:"state"`
}
type Mount struct {
	Database      DatabaseBinding    `json:"database"`
	HostPath      string             `json:"host_path"`
	ContainerPath ContainerMountPath `json:"container_path"`
	BindingID     ReplicaBindingID   `json:"binding_id"`
}
type SchemaState string

const (
	AllocatedEmpty     SchemaState = "allocated_empty"
	VerifiedEmpty      SchemaState = "verified_empty"
	VerifiedSchema     SchemaState = "verified_schema"
	Unknown            SchemaState = "unknown"
	EmptyMarker                    = "brine-empty-v1"
	EmptyCatalogSHA256             = "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"
)

type SchemaObservation struct {
	State         SchemaState `json:"state"`
	DatabaseID    DatabaseID  `json:"database_id"`
	ObservedAt    time.Time   `json:"observed_at"`
	Marker        string      `json:"marker,omitempty"`
	CatalogSHA256 string      `json:"catalog_sha256,omitempty"`
	UnknownReason string      `json:"unknown_reason,omitempty"`
}

var ErrInvalid = errors.New("invalid persistent data record")
var identityPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var filenamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var markerPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func ValidID(id string) bool { return identityPattern.MatchString(id) }
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func ValidRoot(root string) bool {
	return root != "/" && path.IsAbs(root) && path.Clean(root) == root && !strings.ContainsAny(root, "\x00\r\n")
}
func (r RuntimeIdentity) Validate() error {
	if r.UID == 0 || r.UID > 65535 || r.GID == 0 || r.GID > 65535 {
		return ErrInvalid
	}
	return nil
}
func (d Database) Validate() error {
	if !namePattern.MatchString(string(d.Name)) || !namePattern.MatchString(string(d.BackupDestination)) || !ValidRoot(string(d.PersistentRoot)) || !ValidRoot(string(d.MountPath)) || !filenamePattern.MatchString(string(d.Filename)) {
		return ErrInvalid
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal", "-litestream"} {
		if strings.HasSuffix(string(d.Filename), suffix) {
			return ErrInvalid
		}
	}
	for _, forbidden := range []string{"/proc", "/sys", "/dev", "/run", "/etc"} {
		if string(d.MountPath) == forbidden || strings.HasPrefix(string(d.MountPath), forbidden+"/") {
			return ErrInvalid
		}
	}
	return nil
}
func (d Destination) Validate() error {
	u, err := url.Parse(d.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || !namePattern.MatchString(string(d.Reference)) || !namePattern.MatchString(d.CredentialRef) || d.Region == "" || d.Bucket == "" || strings.ContainsAny(d.Bucket, "/\\\x00\r\n") || d.BasePrefix == "" || path.Clean(d.BasePrefix) != d.BasePrefix || strings.HasPrefix(d.BasePrefix, "/") || strings.HasPrefix(d.BasePrefix, "../") || d.BasePrefix == ".." || strings.ContainsAny(d.BasePrefix, "\\\x00\r\n") {
		return ErrInvalid
	}
	return nil
}
func RelativeDirectory(incarnation AppIncarnationID, database DatabaseID) (string, error) {
	if !ValidID(string(incarnation)) || !ValidID(string(database)) {
		return "", ErrInvalid
	}
	return path.Join("apps", string(incarnation), "databases", string(database)), nil
}
func RemotePrefix(base string, incarnation AppIncarnationID, database DatabaseID, epoch ReplicaEpochID) (string, error) {
	if !ValidID(string(epoch)) {
		return "", ErrInvalid
	}
	relative, err := RelativeDirectory(incarnation, database)
	if err != nil {
		return "", err
	}
	if base == "" || path.Clean(base) != base || path.IsAbs(base) || base == ".." || strings.HasPrefix(base, "../") {
		return "", ErrInvalid
	}
	return path.Join(base, relative, "epochs", string(epoch)) + "/", nil
}
