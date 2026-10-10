package data

// MappingProbeImage is pulled only by approved data preparation. Observers and
// mapping probes never implicitly acquire a runtime prerequisite.
const MappingProbeImage = "docker.io/library/python@sha256:2d9aefe2fef018a7eb2c13064c89c71929800fd2e5dccdbf52ea5da5bb8d929a"

// AllocationProposal freezes identities and credential scope without reserving
// anything. Only an approved preparation operation may persist this proposal.
type AllocationProposal struct {
	Database DatabaseBinding `json:"database"`
	Replica  ReplicaBinding  `json:"replica"`
}

func NewAllocationProposal(declaration Database, destination Destination, incarnation AppIncarnationID) (AllocationProposal, error) {
	if declaration.Validate() != nil || destination.Validate() != nil || declaration.BackupDestination != destination.Reference || !ValidID(string(incarnation)) {
		return AllocationProposal{}, ErrInvalid
	}
	database, err := NewID()
	if err != nil {
		return AllocationProposal{}, err
	}
	binding, err := NewID()
	if err != nil {
		return AllocationProposal{}, err
	}
	epoch, err := NewID()
	if err != nil {
		return AllocationProposal{}, err
	}
	relative, err := RelativeDirectory(incarnation, DatabaseID(database))
	if err != nil {
		return AllocationProposal{}, err
	}
	prefix, err := RemotePrefix(destination.BasePrefix, incarnation, DatabaseID(database), ReplicaEpochID(epoch))
	if err != nil {
		return AllocationProposal{}, err
	}
	return AllocationProposal{Database: DatabaseBinding{DatabaseID: DatabaseID(database), Name: declaration.Name, IncarnationID: incarnation, Root: declaration.PersistentRoot, RelativeDirectory: relative, MountPath: declaration.MountPath, Filename: declaration.Filename, ReplicaBindingID: ReplicaBindingID(binding)}, Replica: ReplicaBinding{BindingID: ReplicaBindingID(binding), DatabaseID: DatabaseID(database), Destination: destination, EpochID: ReplicaEpochID(epoch), RemotePrefix: prefix}}, nil
}

func (p AllocationProposal) Valid(declaration Database, destination Destination) bool {
	b, r := p.Database, p.Replica
	relative, err := RelativeDirectory(b.IncarnationID, b.DatabaseID)
	prefix, prefixErr := RemotePrefix(destination.BasePrefix, b.IncarnationID, b.DatabaseID, r.EpochID)
	return declaration.Validate() == nil && destination.Validate() == nil && err == nil && prefixErr == nil && ValidID(string(r.BindingID)) && b.ReplicaBindingID == r.BindingID && b.DatabaseID == r.DatabaseID && b.Name == declaration.Name && b.Root == declaration.PersistentRoot && b.MountPath == declaration.MountPath && b.Filename == declaration.Filename && relative == b.RelativeDirectory && r.Destination == destination && declaration.BackupDestination == destination.Reference && r.RemotePrefix == prefix && !r.Committed && r.CredentialVersion == 0 && r.CredentialFile == "" && r.ConfigContent == "" && r.ConfigFile == "" && r.ConfigSHA256 == "" && r.UnitSHA256 == "" && r.SocketFile == "" && r.LifetimeLockFile == ""
}
