package restic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type Client struct {
	toolPath   string
	toolSHA256 string
	limits     Limits
	admission  RepositoryAdmission
}

type RepositoryAction string

const (
	CreateRepository RepositoryAction = "create"
	WriteSnapshot    RepositoryAction = "backup"
	ReadRepository   RepositoryAction = "read"
)

// RepositoryAdmission re-reads protected ownership, lease, credential and
// reviewed creation facts under the caller's existing locks. No host/store
// implementation is supplied by this transport core.
type RepositoryAdmission interface {
	AdmitRepository(context.Context, Scope, RepositoryAction, string) error
}

func NewClient(toolSHA256 string, limits Limits, admission RepositoryAdmission) (*Client, error) {
	if !hashPattern.MatchString(toolSHA256) || !limits.valid() || admission == nil {
		return nil, ErrInvalid
	}
	return &Client{toolPath: ToolPath, toolSHA256: toolSHA256, limits: limits, admission: admission}, nil
}

// Init requires the separately reviewed new-owned-repository grant carried by
// Repository. Callers must reconcile an unknown result before any retry.
func (c *Client) Init(ctx context.Context, repository Repository, workspace string) (InitializedRepository, error) {
	if !hashPattern.MatchString(repository.initializationPlan) || repository.scope.RepositoryID != "" {
		return InitializedRepository{}, ErrInvalid
	}
	raw, err := c.jsonCommand(ctx, repository, workspace, true, nil, []string{"init", "--repository-version", "2"})
	if err != nil {
		return InitializedRepository{}, err
	}
	id, err := parseInit(raw)
	if err != nil {
		return InitializedRepository{}, ErrUnknown
	}
	observed, err := c.InspectInitialization(ctx, repository, workspace)
	if err != nil || observed.repository.scope.RepositoryID != id {
		return InitializedRepository{}, ErrUnknown
	}
	return observed, nil
}

// InspectInitialization reconciles an issued initialization by authenticated
// config readback under its retained creation grant. It never repeats init.
func (c *Client) InspectInitialization(ctx context.Context, repository Repository, workspace string) (InitializedRepository, error) {
	if !hashPattern.MatchString(repository.initializationPlan) || repository.scope.RepositoryID != "" {
		return InitializedRepository{}, ErrInvalid
	}
	raw, err := c.jsonCommand(ctx, repository, workspace, false, nil, []string{"cat", "config"})
	if err != nil {
		return InitializedRepository{}, err
	}
	id, err := parseRepositoryConfig(raw)
	if err != nil {
		return InitializedRepository{}, err
	}
	grant := repository.initializationPlan
	repository.scope.RepositoryID = id
	repository.initializationPlan = ""
	return InitializedRepository{repository: repository, creationGrant: grant, configSHA256: digest(raw), verifiedAt: time.Now().UTC()}, nil
}

// RepositoryIdentity authenticates the encrypted config through the current
// protected repository/key scope. It never returns its location or key material.
func (c *Client) RepositoryIdentity(ctx context.Context, repository Repository, workspace string) (string, error) {
	if repository.initializationPlan != "" || !hashPattern.MatchString(repository.scope.RepositoryID) {
		return "", ErrInvalid
	}
	raw, err := c.jsonCommand(ctx, repository, workspace, false, nil, []string{"cat", "config"})
	if err != nil {
		return "", err
	}
	id, err := parseRepositoryConfig(raw)
	if err != nil || id != repository.scope.RepositoryID {
		return "", ErrIntegrity
	}
	return id, nil
}

func (r Repository) RepositoryID() string { return r.scope.RepositoryID }

func (c *Client) Backup(ctx context.Context, repository Repository, workspace string, bundle Bundle) (TransportPoint, error) {
	if err := bundle.manifest.admit(c.limits); err != nil {
		return TransportPoint{}, err
	}
	if repository.initializationPlan != "" || repository.scope.BindingID != bundle.manifest.document.Identity.BindingID || repository.scope.EpochID != bundle.manifest.document.Identity.EpochID {
		return TransportPoint{}, ErrInvalid
	}
	if _, err := c.RepositoryIdentity(ctx, repository, workspace); err != nil {
		return TransportPoint{}, err
	}
	file, err := bundle.openVerified(ctx)
	if err != nil {
		return TransportPoint{}, err
	}
	defer func() { _ = file.Close() }()
	args := []string{"backup", "--stdin", "--stdin-filename", archiveName, "--host", "brine", "--tag", captureTag(bundle.manifest.document.Identity)}
	raw, err := c.jsonCommand(ctx, repository, workspace, true, file, args)
	if err != nil {
		return TransportPoint{}, err
	}
	id, err := parseBackup(raw, bundle.size)
	if err != nil {
		return TransportPoint{}, ErrUnknown
	}
	if err := bundle.verify(ctx); err != nil {
		return TransportPoint{}, ErrUnknown
	}
	if _, err := c.RepositoryIdentity(ctx, repository, workspace); err != nil {
		return TransportPoint{}, ErrUnknown
	}
	return TransportPoint{Identity: bundle.manifest.document.Identity, Scope: repository.scope, SnapshotID: id, ManifestSHA256: bundle.manifest.SHA256(), ArchiveSHA256: bundle.sha256, ArchiveBytes: bundle.size}, nil
}

func captureTag(identity Identity) string { return "brine-capture-" + identity.CaptureID }

func (c *Client) InspectExact(ctx context.Context, repository Repository, workspace string, point TransportPoint) error {
	if !point.valid(c.limits) || !sameRepository(repository, point) {
		return ErrInvalid
	}
	if _, err := c.RepositoryIdentity(ctx, repository, workspace); err != nil {
		return err
	}
	raw, err := c.jsonCommand(ctx, repository, workspace, false, nil, []string{"ls", point.SnapshotID})
	if err != nil {
		return err
	}
	return parseListing(raw, point)
}

// InspectCapture resolves an interrupted backup by its immutable capture tag.
// It refuses duplicates and then checks the exact returned snapshot. It never
// selects latest or restarts a write.
func (c *Client) InspectCapture(ctx context.Context, repository Repository, workspace string, bundle Bundle) (TransportPoint, error) {
	if err := bundle.manifest.admit(c.limits); err != nil {
		return TransportPoint{}, err
	}
	if err := bundle.verify(ctx); err != nil {
		return TransportPoint{}, err
	}
	if _, err := c.RepositoryIdentity(ctx, repository, workspace); err != nil {
		return TransportPoint{}, err
	}
	raw, err := c.jsonCommand(ctx, repository, workspace, false, nil, []string{"snapshots", "--tag", captureTag(bundle.manifest.document.Identity)})
	if err != nil {
		return TransportPoint{}, err
	}
	id, err := parseSnapshots(raw, bundle.manifest.document.Identity)
	if err != nil {
		return TransportPoint{}, err
	}
	point := TransportPoint{Identity: bundle.manifest.document.Identity, Scope: repository.scope, SnapshotID: id, ManifestSHA256: bundle.manifest.SHA256(), ArchiveSHA256: bundle.sha256, ArchiveBytes: bundle.size}
	if err := c.InspectExact(ctx, repository, workspace, point); err != nil {
		return TransportPoint{}, err
	}
	return point, nil
}

func sameRepository(repository Repository, point TransportPoint) bool {
	return repository.initializationPlan == "" && repository.scope.BindingID == point.Scope.BindingID && repository.scope.EpochID == point.Scope.EpochID && repository.scope.RepositoryID == point.Scope.RepositoryID && repository.scope.DestinationSHA256 == point.Scope.DestinationSHA256 && repository.scope.RecoveryVersion == point.Scope.RecoveryVersion
}

func (c *Client) RestoreExact(ctx context.Context, repository Repository, workspace, directory string, point TransportPoint, manifest Manifest) error {
	if err := manifest.admit(c.limits); err != nil {
		return err
	}
	if !point.valid(c.limits) || manifest.SHA256() != point.ManifestSHA256 || manifest.document.Identity != point.Identity || point.ArchiveBytes != envelopeSize(manifest) || !sameRepository(repository, point) {
		return ErrInvalid
	}
	if err := privateDirectory(workspace); err != nil {
		return err
	}
	if err := c.InspectExact(ctx, repository, workspace, point); err != nil {
		return err
	}
	file, err := os.CreateTemp(workspace, "downloaded-*.tar")
	if err != nil {
		return ErrFailed
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer func() { _ = file.Close() }()
	output := &quotaWriter{writer: file, remaining: point.ArchiveBytes}
	if err := c.command(ctx, repository, workspace, false, nil, output, []string{"dump", point.SnapshotID, "/" + archiveName}, false); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || info.Size() != point.ArchiveBytes {
		return ErrIntegrity
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ErrFailed
	}
	hash, err := hashFile(ctx, file, info.Size())
	if err != nil || hash != point.ArchiveSHA256 {
		return ErrIntegrity
	}
	if err := file.Sync(); err != nil {
		return ErrFailed
	}
	return extractEnvelope(ctx, file.Name(), directory, manifest, c.limits)
}

func (c *Client) Check(ctx context.Context, repository Repository, workspace string) error {
	if _, err := c.RepositoryIdentity(ctx, repository, workspace); err != nil {
		return err
	}
	raw, err := c.jsonCommand(ctx, repository, workspace, false, nil, []string{"check", "--read-data"})
	if err != nil {
		return err
	}
	return parseCheck(raw)
}

func (c *Client) jsonCommand(ctx context.Context, repository Repository, workspace string, mutation bool, input io.Reader, args []string) ([]byte, error) {
	var output bytes.Buffer
	if err := c.command(ctx, repository, workspace, mutation, input, &quotaWriter{writer: &output, remaining: jsonLimit}, args, true); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (c *Client) command(ctx context.Context, repository Repository, workspace string, mutation bool, input io.Reader, output io.Writer, args []string, jsonOutput bool) error {
	deadline, ok := ctx.Deadline()
	if !ok || ctx.Err() != nil || time.Until(deadline) > 30*time.Minute {
		return ErrInvalid
	}
	identity := Identity{BindingID: repository.scope.BindingID, EpochID: repository.scope.EpochID}
	if err := repository.admit(deadline, identity); err != nil {
		return err
	}
	action := ReadRepository
	if mutation {
		action = WriteSnapshot
	}
	if mutation && repository.initializationPlan != "" {
		action = CreateRepository
	}
	if c.admission == nil || c.admission.AdmitRepository(ctx, repository.scope, action, repository.initializationPlan) != nil {
		return ErrInvalid
	}
	if err := privateDirectory(workspace); err != nil {
		return err
	}
	directory, err := os.MkdirTemp(workspace, "restic-")
	if err != nil {
		return ErrFailed
	}
	defer func() { _ = os.RemoveAll(directory) }()
	if err := c.checkTool(ctx, directory); err != nil {
		return err
	}
	environment := append(privateEnvironment(directory), repository.private()...)
	fixed := []string{"--no-lock", "--no-cache"}
	if jsonOutput {
		fixed = append(fixed, "--json")
	}
	if repository.pathStyle {
		fixed = append(fixed, "-o", "s3.bucket-lookup=path")
	}
	command := localexec.Command{Path: c.toolPath, Dir: directory, Env: environment, Args: append(fixed, args...), OutputLimit: 64 << 10, Mutation: mutation}
	if err := (localexec.ExecRunner{}).RunStreams(ctx, command, input, output); err != nil {
		return commandError(err)
	}
	return nil
}

func privateEnvironment(directory string) []string {
	return []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "HOME=" + directory, "TMPDIR=" + directory, "RESTIC_CACHE_DIR=" + directory, "AWS_EC2_METADATA_DISABLED=true"}
}

func (c *Client) checkTool(ctx context.Context, directory string) error {
	for p := c.toolPath; ; p = filepath.Dir(p) {
		// #nosec G703 -- Fixed production path; root-owned no-follow parents are the check.
		info, err := os.Lstat(p)
		if err != nil || !protectedNode(info, p == c.toolPath) {
			return ErrTool
		}
		if p == "/" {
			break
		}
	}
	// #nosec G703 -- Fixed protected executable, verified root-owned parents above.
	file, err := os.Open(c.toolPath)
	if err != nil {
		return ErrTool
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || info.Size() > 128<<20 {
		return ErrTool
	}
	hash, err := hashFile(ctx, file, info.Size())
	if err != nil || hash != c.toolSHA256 {
		return ErrTool
	}
	var output bytes.Buffer
	command := localexec.Command{Path: c.toolPath, Dir: directory, Env: privateEnvironment(directory), Args: []string{"version"}, OutputLimit: 1024}
	if err := (localexec.ExecRunner{}).RunStreams(ctx, command, nil, &quotaWriter{writer: &output, remaining: 1024}); err != nil {
		return ErrTool
	}
	if !strings.HasPrefix(output.String(), "restic "+Version+" compiled with ") || strings.Count(output.String(), "\n") != 1 {
		return ErrTool
	}
	return nil
}

func commandError(err error) error {
	var operation *localexec.Error
	if errors.As(err, &operation) && operation.Kind == localexec.UnknownOutcome {
		return ErrUnknown
	}
	return ErrFailed
}
