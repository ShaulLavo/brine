package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
)

type DataReservation struct {
	App         string
	PolicyHash  string
	Database    data.Database
	Destination data.Destination
}
type ReservedDatabase struct {
	Database data.DatabaseBinding
	Replica  data.ReplicaBinding
}
type ReplicaPermit struct {
	DBPath               string                 `json:"db_path"`
	SocketPath           string                 `json:"socket_path"`
	ConfigPath           string                 `json:"config_path"`
	CredentialPath       string                 `json:"credential_path"`
	LifetimeLockPath     string                 `json:"lifetime_lock_path"`
	FenceState           string                 `json:"fence_state"`
	DestinationOwnership string                 `json:"destination_ownership"`
	SourceSettled        bool                   `json:"source_settled"`
	Database             data.DatabaseBinding   `json:"database"`
	Replica              data.ReplicaBinding    `json:"replica"`
	Fences               []data.QuiescenceFence `json:"fences"`
}

func (p ReplicaPermit) AllowsReplica(binding data.ReplicaBindingID, epoch data.ReplicaEpochID, configSHA256 string) bool {
	if !p.SourceSettled || p.DestinationOwnership != "local" || p.FenceState == "held" || !p.Replica.Committed || p.Replica.BindingID != binding || p.Replica.EpochID != epoch || p.Replica.ConfigSHA256 != configSHA256 || !digestPattern.MatchString("sha256:"+configSHA256) || p.Database.DatabaseID != p.Replica.DatabaseID || p.Database.ReplicaBindingID != binding || p.Fences == nil {
		return false
	}
	for _, f := range p.Fences {
		if f.DatabaseID != p.Database.DatabaseID || f.IncarnationID != p.Database.IncarnationID || (f.State != data.FenceHeld && f.State != data.FenceReleased) || f.State == data.FenceHeld {
			return false
		}
	}
	return true
}

// ReserveDatabase allocates identities exactly once. The caller holds the host
// mutation lock. This writes no directories and grants no startup permission.
func (s *Store) ReserveDatabase(ctx context.Context, req DataReservation) (ReservedDatabase, error) {
	if req.App == "" || len(req.App) > 63 || !digestPattern.MatchString(req.PolicyHash) || req.Database.Validate() != nil || req.Destination.Validate() != nil || req.Database.BackupDestination != req.Destination.Reference {
		return ReservedDatabase{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReservedDatabase{}, err
	}
	defer tx.Rollback()
	var incarnation, policyHash string
	err = tx.QueryRowContext(ctx, "SELECT id,policy_hash FROM data_incarnations WHERE app=?", req.App).Scan(&incarnation, &policyHash)
	if err == nil && policyHash != req.PolicyHash {
		return ReservedDatabase{}, ErrConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		incarnation, err = data.NewID()
		if err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO data_incarnations VALUES(?,?,?)", incarnation, req.App, req.PolicyHash)
		}
	}
	if err != nil {
		return ReservedDatabase{}, err
	}
	var dbRaw, replicaRaw []byte
	err = tx.QueryRowContext(ctx, "SELECT d.canonical,b.canonical FROM data_databases d JOIN data_replica_bindings b ON b.database_id=d.id WHERE d.incarnation_id=? AND d.name=?", incarnation, req.Database.Name).Scan(&dbRaw, &replicaRaw)
	if err == nil {
		var existing ReservedDatabase
		if json.Unmarshal(dbRaw, &existing.Database) != nil || json.Unmarshal(replicaRaw, &existing.Replica) != nil {
			return ReservedDatabase{}, &IntegrityError{}
		}
		if existing.Database.Root != req.Database.PersistentRoot || existing.Database.MountPath != req.Database.MountPath || existing.Database.Filename != req.Database.Filename || existing.Replica.Destination != req.Destination {
			return ReservedDatabase{}, ErrConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ReservedDatabase{}, err
	}
	database, err := data.NewID()
	if err != nil {
		return ReservedDatabase{}, err
	}
	binding, err := data.NewID()
	if err != nil {
		return ReservedDatabase{}, err
	}
	epoch, err := data.NewID()
	if err != nil {
		return ReservedDatabase{}, err
	}
	relative, err := data.RelativeDirectory(data.AppIncarnationID(incarnation), data.DatabaseID(database))
	if err != nil {
		return ReservedDatabase{}, err
	}
	prefix, err := data.RemotePrefix(req.Destination.BasePrefix, data.AppIncarnationID(incarnation), data.DatabaseID(database), data.ReplicaEpochID(epoch))
	if err != nil {
		return ReservedDatabase{}, err
	}
	if err = checkDestinationOwner(ctx, tx, req.Destination.Endpoint, req.Destination.Bucket, prefix, data.ReplicaBindingID(binding)); err != nil {
		return ReservedDatabase{}, err
	}
	reserved := ReservedDatabase{Database: data.DatabaseBinding{DatabaseID: data.DatabaseID(database), Name: req.Database.Name, IncarnationID: data.AppIncarnationID(incarnation), Root: req.Database.PersistentRoot, RelativeDirectory: relative, MountPath: req.Database.MountPath, Filename: req.Database.Filename, ReplicaBindingID: data.ReplicaBindingID(binding)}, Replica: data.ReplicaBinding{BindingID: data.ReplicaBindingID(binding), DatabaseID: data.DatabaseID(database), Destination: req.Destination, EpochID: data.ReplicaEpochID(epoch), RemotePrefix: prefix}}
	dbRaw, err = json.Marshal(reserved.Database)
	if err != nil {
		return ReservedDatabase{}, err
	}
	replicaRaw, err = json.Marshal(reserved.Replica)
	if err != nil {
		return ReservedDatabase{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_databases VALUES(?,?,?,?,?,?)", database, incarnation, req.Database.Name, req.Database.PersistentRoot, relative, dbRaw); err != nil {
		return ReservedDatabase{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_replica_bindings VALUES(?,?,?,?,?,?,?)", binding, database, epoch, req.Destination.Endpoint, req.Destination.Bucket, prefix, replicaRaw); err != nil {
		return ReservedDatabase{}, err
	}
	return reserved, tx.Commit()
}

type dataQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func checkDestinationOwner(ctx context.Context, q dataQuerier, endpoint, bucket, prefix string, owner data.ReplicaBindingID) error {
	rows, err := q.QueryContext(ctx, "SELECT id,prefix FROM data_replica_bindings WHERE endpoint=? AND bucket=?", endpoint, bucket)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, existing string
		if err = rows.Scan(&id, &existing); err != nil {
			return err
		}
		if id != string(owner) && (prefix == existing || strings.HasPrefix(prefix, existing) || strings.HasPrefix(existing, prefix)) {
			return ErrConflict
		}
	}
	return rows.Err()
}
func (s *Store) CheckReplicaDestinationOwner(ctx context.Context, endpoint, bucket, prefix string, owner data.ReplicaBindingID) error {
	if !data.ValidID(string(owner)) || prefix == "" {
		return ErrInvalid
	}
	return checkDestinationOwner(ctx, s.db, endpoint, bucket, prefix, owner)
}
func readReplicaPermit(ctx context.Context, q dataQuerier, id data.DatabaseID) (ReplicaPermit, error) {
	var p ReplicaPermit
	var dbRaw, raw []byte
	err := q.QueryRowContext(ctx, "SELECT d.canonical,COALESCE((SELECT c.canonical FROM data_replica_commits c WHERE c.binding_id=b.id ORDER BY c.version DESC LIMIT 1),b.canonical) FROM data_databases d JOIN data_replica_bindings b ON b.database_id=d.id WHERE d.id=?", id).Scan(&dbRaw, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if json.Unmarshal(dbRaw, &p.Database) != nil || json.Unmarshal(raw, &p.Replica) != nil || p.Database.DatabaseID != id || p.Replica.DatabaseID != id || p.Database.ReplicaBindingID != p.Replica.BindingID {
		return p, &IntegrityError{}
	}
	rows, err := q.QueryContext(ctx, "SELECT id,incarnation_id,database_id,operation_id,state FROM data_fences WHERE database_id=? ORDER BY id", id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	p.Fences = []data.QuiescenceFence{}
	for rows.Next() {
		var f data.QuiescenceFence
		if err = rows.Scan(&f.ID, &f.IncarnationID, &f.DatabaseID, &f.OperationID, &f.State); err != nil {
			return p, err
		}
		p.Fences = append(p.Fences, f)
	}
	if err = rows.Err(); err != nil {
		return p, err
	}
	p.DBPath = path.Join(string(p.Database.Root), p.Database.RelativeDirectory, string(p.Database.Filename))
	p.SocketPath = p.Replica.SocketFile
	p.ConfigPath = p.Replica.ConfigFile
	p.CredentialPath = p.Replica.CredentialFile
	p.LifetimeLockPath = p.Replica.LifetimeLockFile
	p.FenceState = "unfenced"
	for _, f := range p.Fences {
		if f.State == data.FenceHeld {
			p.FenceState = "held"
			break
		}
		p.FenceState = "released"
	}
	p.DestinationOwnership = "local"
	// A committed binding is the only settled source state until live restore
	// introduces a separately journaled replacement transition.
	p.SourceSettled = p.Replica.Committed
	return p, nil
}

// ReadReplicaPermit reads one consistent snapshot without taking the host lock.
// Missing, unreadable or uncommitted state is never an affirmative permit.
func (s *Store) ReadReplicaPermit(ctx context.Context, id data.DatabaseID) (ReplicaPermit, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ReplicaPermit{}, err
	}
	defer tx.Rollback()
	p, err := readReplicaPermit(ctx, tx, id)
	if err != nil {
		return p, err
	}
	if err = s.validateReplicaPermit(ctx, tx, p); err != nil {
		return p, err
	}
	return p, tx.Commit()
}

// CommitReplicaBinding appends activation evidence under the host mutation lock.
// Destination, database and epoch identities cannot change through this API.
func (s *Store) CommitReplicaBinding(ctx context.Context, b data.ReplicaBinding) error {
	if !digestPattern.MatchString("sha256:"+b.ConfigSHA256) || !digestPattern.MatchString("sha256:"+b.UnitSHA256) || b.CredentialVersion == 0 || !data.ValidRoot(b.CredentialFile) {
		return ErrInvalid
	}
	sum := sha256.Sum256([]byte(b.ConfigContent))
	if len(b.ConfigContent) == 0 || len(b.ConfigContent) > 65536 || hex.EncodeToString(sum[:]) != b.ConfigSHA256 {
		return ErrInvalid
	}
	expectedDir := path.Join(s.dir, "replication", string(b.BindingID))
	if b.ConfigFile != path.Join(expectedDir, "litestream.yml") || b.SocketFile != path.Join(expectedDir, "control.sock") || b.LifetimeLockFile != path.Join(s.dir, "replica-locks", string(b.BindingID)+".lock") || b.CredentialFile != path.Join(s.dir, "credentials", "s3", b.Destination.CredentialRef, "v"+strconv.FormatUint(b.CredentialVersion, 10)+".env") {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := readReplicaPermit(ctx, tx, b.DatabaseID)
	if err != nil {
		return err
	}
	original := p.Replica
	if original.BindingID != b.BindingID || original.DatabaseID != b.DatabaseID || original.EpochID != b.EpochID || original.RemotePrefix != b.RemotePrefix || original.Destination != b.Destination {
		return ErrConflict
	}
	for _, f := range p.Fences {
		if f.State == data.FenceHeld {
			return ErrConflict
		}
	}
	b.Committed = true
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	previous, err := json.Marshal(original)
	if err != nil {
		return err
	}
	if bytes.Equal(raw, previous) {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_replica_commits SELECT ?,COALESCE(MAX(version),0)+1,? FROM data_replica_commits WHERE binding_id=?", b.BindingID, raw, b.BindingID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) HoldDataFence(ctx context.Context, id data.DatabaseID, operation string) (data.QuiescenceFence, error) {
	if operation == "" || len(operation) > 128 {
		return data.QuiescenceFence{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	defer tx.Rollback()
	p, err := readReplicaPermit(ctx, tx, id)
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	for _, f := range p.Fences {
		if f.State == data.FenceHeld {
			if f.OperationID != operation {
				return f, ErrConflict
			}
			return f, tx.Commit()
		}
	}
	newID, err := data.NewID()
	if err != nil {
		return data.QuiescenceFence{}, err
	}
	f := data.QuiescenceFence{ID: data.FenceID(newID), DatabaseID: id, IncarnationID: p.Database.IncarnationID, OperationID: operation, State: data.FenceHeld}
	if _, err = tx.ExecContext(ctx, "INSERT INTO data_fences VALUES(?,?,?,?,?)", f.ID, f.DatabaseID, f.IncarnationID, f.OperationID, f.State); err != nil {
		return f, err
	}
	return f, tx.Commit()
}

// ReleaseDataFence must follow fresh schema/quiescence checks by the operation
// owner. Reconciliation must not call it merely because an upload exists.
func (s *Store) ReleaseDataFence(ctx context.Context, id data.FenceID, operation string) error {
	result, err := s.db.ExecContext(ctx, "UPDATE data_fences SET state='released' WHERE id=? AND operation_id=? AND state='held'", id, operation)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

type CredentialScope struct {
	App        string               `json:"app"`
	PolicyHash string               `json:"policy_hash"`
	Database   data.DatabaseBinding `json:"database"`
	Replica    data.ReplicaBinding  `json:"replica"`
	FenceHeld  bool                 `json:"fence_held"`
}

// ReadCredentialScopes is read-only. Delivery and activation callers retain the
// host mutation lock while rechecking policy and using this snapshot.
func (s *Store) ReadCredentialScopes(ctx context.Context, app string) ([]CredentialScope, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT d.id,i.policy_hash FROM data_databases d JOIN data_incarnations i ON i.id=d.incarnation_id WHERE i.app=? ORDER BY d.name", app)
	if err != nil {
		return nil, err
	}
	type identity struct {
		id   data.DatabaseID
		hash string
	}
	ids := []identity{}
	for rows.Next() {
		var i identity
		if err = rows.Scan(&i.id, &i.hash); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrNotFound
	}
	scopes := make([]CredentialScope, 0, len(ids))
	for _, i := range ids {
		p, err := readReplicaPermit(ctx, tx, i.id)
		if err != nil {
			return nil, err
		}
		scope := CredentialScope{App: app, PolicyHash: i.hash, Database: p.Database, Replica: p.Replica}
		for _, f := range p.Fences {
			if f.State == data.FenceHeld {
				scope.FenceHeld = true
			}
		}
		scopes = append(scopes, scope)
	}
	return scopes, tx.Commit()
}

// CredentialRecord is deliberately incapable of carrying credential values.
// P04-03 owns delivery and presentation types; this is their durable reference.
type CredentialRecord struct {
	// Requester comes only from the authenticated dispatcher identity, never
	// from a request packet or credential delivery payload.
	Requester   string                    `json:"requester"`
	ID          string                    `json:"id"`
	Kind        string                    `json:"kind"`
	PlanID      string                    `json:"plan_id"`
	PlanHash    string                    `json:"plan_hash"`
	TargetHash  string                    `json:"target_hash"`
	PolicyHash  string                    `json:"policy_hash"`
	BindingID   data.ReplicaBindingID     `json:"binding_id"`
	Destination data.BackupDestinationRef `json:"destination"`
	EpochID     data.ReplicaEpochID       `json:"epoch_id"`
	Version     uint64                    `json:"version"`
	ReceivedAt  time.Time                 `json:"received_at"`
	ExpiresAt   *time.Time                `json:"expires_at,omitempty"`
}

func (s *Store) SaveCredentialRecord(ctx context.Context, r CredentialRecord) error {
	if r.Requester == "" || len(r.Requester) > 256 || !data.ValidID(r.ID) || !data.ValidID(string(r.BindingID)) || !data.ValidID(string(r.EpochID)) || (r.Kind != "plan" && r.Kind != "receipt") || r.PlanID == "" || !digestPattern.MatchString(r.PolicyHash) || !digestPattern.MatchString(r.PlanHash) || !digestPattern.MatchString(r.TargetHash) || (r.Kind == "receipt" && (r.Version == 0 || r.ReceivedAt.IsZero())) {
		return ErrInvalid
	}
	var database data.DatabaseID
	if err := s.db.QueryRowContext(ctx, "SELECT database_id FROM data_replica_bindings WHERE id=?", r.BindingID).Scan(&database); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	p, err := s.ReadReplicaPermit(ctx, database)
	if err != nil {
		return err
	}
	if p.Replica.EpochID != r.EpochID || p.Replica.Destination.Reference != r.Destination {
		return ErrConflict
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO data_credential_records VALUES(?,?,?,?)", r.ID, r.BindingID, r.Kind, raw)
	return err
}
func (s *Store) LoadCredentialRecord(ctx context.Context, id string) (CredentialRecord, error) {
	var r CredentialRecord
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT canonical FROM data_credential_records WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if json.Unmarshal(raw, &r) != nil || r.ID != id {
		return r, &IntegrityError{}
	}
	return r, nil
}
func (r ReservedDatabase) Mount() data.Mount {
	return data.Mount{Database: r.Database, HostPath: path.Join(string(r.Database.Root), r.Database.RelativeDirectory), ContainerPath: r.Database.MountPath, BindingID: r.Replica.BindingID}
}

// ReadReplicaPermitByBinding projects the same read-only snapshot for unit gates.
func (s *Store) ReadReplicaPermitByBinding(ctx context.Context, id data.ReplicaBindingID) (ReplicaPermit, error) {
	var database data.DatabaseID
	err := s.db.QueryRowContext(ctx, "SELECT database_id FROM data_replica_bindings WHERE id=?", id).Scan(&database)
	if errors.Is(err, sql.ErrNoRows) {
		return ReplicaPermit{}, ErrNotFound
	}
	if err != nil {
		return ReplicaPermit{}, err
	}
	return s.ReadReplicaPermit(ctx, database)
}

// ReadWriterPermits returns every database of an incarnation in one snapshot.
// Schema compatibility is deliberately not a cached field in this projection.
func (s *Store) ReadWriterPermits(ctx context.Context, id data.AppIncarnationID) ([]ReplicaPermit, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id FROM data_databases WHERE incarnation_id=? ORDER BY name", id)
	if err != nil {
		return nil, err
	}
	ids := []data.DatabaseID{}
	for rows.Next() {
		var database data.DatabaseID
		if err = rows.Scan(&database); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, database)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrNotFound
	}
	permits := make([]ReplicaPermit, 0, len(ids))
	for _, database := range ids {
		p, err := readReplicaPermit(ctx, tx, database)
		if err != nil {
			return nil, err
		}
		if err = s.validateReplicaPermit(ctx, tx, p); err != nil {
			return nil, err
		}
		permits = append(permits, p)
	}
	return permits, tx.Commit()
}

func (s *Store) validateReplicaPermit(ctx context.Context, q dataQuerier, p ReplicaPermit) error {
	d, b := p.Database, p.Replica
	if !data.ValidID(string(d.IncarnationID)) || !data.ValidID(string(d.DatabaseID)) || !data.ValidID(string(b.BindingID)) || !data.ValidID(string(b.EpochID)) {
		return &IntegrityError{}
	}
	declaration := data.Database{Name: d.Name, PersistentRoot: d.Root, MountPath: d.MountPath, Filename: d.Filename, BackupDestination: b.Destination.Reference}
	if declaration.Validate() != nil || b.Destination.Validate() != nil {
		return &IntegrityError{}
	}
	relative, err := data.RelativeDirectory(d.IncarnationID, d.DatabaseID)
	if err != nil || relative != d.RelativeDirectory {
		return &IntegrityError{}
	}
	prefix, err := data.RemotePrefix(b.Destination.BasePrefix, d.IncarnationID, d.DatabaseID, b.EpochID)
	if err != nil || prefix != b.RemotePrefix {
		return &IntegrityError{}
	}
	if err = checkDestinationOwner(ctx, q, b.Destination.Endpoint, b.Destination.Bucket, b.RemotePrefix, b.BindingID); err != nil {
		return err
	}
	for _, f := range p.Fences {
		if !data.ValidID(string(f.ID)) || f.DatabaseID != d.DatabaseID || f.IncarnationID != d.IncarnationID || f.OperationID == "" || (f.State != data.FenceHeld && f.State != data.FenceReleased) {
			return &IntegrityError{}
		}
	}
	if !b.Committed {
		return nil
	}
	sum := sha256.Sum256([]byte(b.ConfigContent))
	expected := path.Join(s.dir, "replication", string(b.BindingID))
	if len(b.ConfigContent) == 0 || hex.EncodeToString(sum[:]) != b.ConfigSHA256 || !digestPattern.MatchString("sha256:"+b.UnitSHA256) || b.ConfigFile != path.Join(expected, "litestream.yml") || b.SocketFile != path.Join(expected, "control.sock") || b.LifetimeLockFile != path.Join(s.dir, "replica-locks", string(b.BindingID)+".lock") || b.CredentialVersion == 0 || b.CredentialFile != path.Join(s.dir, "credentials", "s3", b.Destination.CredentialRef, "v"+strconv.FormatUint(b.CredentialVersion, 10)+".env") {
		return &IntegrityError{}
	}
	return nil
}
