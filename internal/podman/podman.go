// Package podman exposes only the runtime operations needed by deployment.
package podman

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type Image struct{ value string }

func (i Image) String() string { return i.value }

type Name struct{ value string }

func (n Name) String() string { return n.value }

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,252}$`)
var secretNamePattern = regexp.MustCompile(`^brine\.[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?\.[A-Za-z0-9][A-Za-z0-9_-]{0,252}\.v[1-9][0-9]*$`)
var imagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*(?::[0-9]+)?(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)+(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?@sha256:[a-fA-F0-9]{64}$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func ParseImage(s string) (Image, error) {
	if len(s) > 1024 || !imagePattern.MatchString(s) {
		return Image{}, invalid()
	}
	prefix, digest, _ := strings.Cut(s, "@sha256:")
	return Image{prefix + "@sha256:" + strings.ToLower(digest)}, nil
}
func ParseName(s string) (Name, error) {
	if !namePattern.MatchString(s) {
		return Name{}, invalid()
	}
	return Name{s}, nil
}

// ParseSecretName accepts immutable Brine names, not generic runtime names.
func ParseSecretName(s string) (Name, error) {
	if !secretNamePattern.MatchString(s) {
		return Name{}, invalid()
	}
	version := s[strings.LastIndex(s, ".v")+2:]
	if _, err := strconv.ParseUint(version, 10, 64); err != nil {
		return Name{}, invalid()
	}
	return ParseName(s)
}
func invalid() error   { return &localexec.Error{Kind: localexec.Invalid} }
func malformed() error { return &localexec.Error{Kind: localexec.Failed} }

type Platform struct {
	OS           string
	Architecture string
	Variant      string
}
type Version struct {
	Version  string
	Platform Platform
}

// IndexDigest is the requested pin. For a single-platform image it is the
// manifest digest itself; a multi-platform index has a separate ManifestDigest.
type ImageInfo struct {
	ImageID        string
	IndexDigest    string
	ManifestDigest string
	Platform       Platform
}
type ContainerState struct {
	Status   string
	Running  bool
	ExitCode int
}

type Adapter interface {
	Version(context.Context) (Version, error)
	Pull(context.Context, Image) error
	Inspect(context.Context, Image) (ImageInfo, error)
	ImageExists(context.Context, Image) (bool, error)
	CreateSecret(context.Context, Name, []byte) error
	SecretExists(context.Context, Name) (bool, error)
	SecretNames(context.Context) ([]Name, error)
	ContainerState(context.Context, Name) (ContainerState, error)
}

type Client struct{ session localexec.Session }

func New(session localexec.Session) *Client { return &Client{session} }
func (c *Client) run(ctx context.Context, args []string, stdin []byte, mutation bool) (localexec.Result, error) {
	return c.session.Execute(ctx, "podman", args, stdin, mutation)
}
func (c *Client) Version(ctx context.Context) (Version, error) {
	r, e := c.run(ctx, []string{"version", "--format", "json"}, nil, false)
	if e != nil {
		return Version{}, e
	}
	var data struct {
		Client struct {
			Version string
			OsArch  string
		}
	}
	if json.Unmarshal([]byte(r.Stdout), &data) != nil || data.Client.Version == "" {
		return Version{}, malformed()
	}
	parts := strings.Split(data.Client.OsArch, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Version{}, malformed()
	}
	return Version{Version: data.Client.Version, Platform: Platform{OS: parts[0], Architecture: parts[1]}}, nil
}
func (c *Client) Pull(ctx context.Context, image Image) error {
	if image.value == "" {
		return invalid()
	}
	_, e := c.run(ctx, []string{"pull", image.value}, nil, true)
	return e
}
func (c *Client) ImageExists(ctx context.Context, image Image) (bool, error) {
	if image.value == "" {
		return false, invalid()
	}
	return c.exists(ctx, []string{"image", "exists", image.value})
}
func (c *Client) SecretExists(ctx context.Context, name Name) (bool, error) {
	if name.value == "" {
		return false, invalid()
	}
	return c.exists(ctx, []string{"secret", "exists", name.value})
}
func (c *Client) exists(ctx context.Context, args []string) (bool, error) {
	_, e := c.run(ctx, args, nil, false)
	if e == nil {
		return true, nil
	}
	var re *localexec.Error
	if errors.As(e, &re) && re.Kind == localexec.Failed && re.ExitCode == 1 {
		return false, nil
	}
	return false, e
}

// CreateSecret borrows input for the synchronous call. The caller owns the slice
// and its best-effort clearing after the call returns.
func (c *Client) CreateSecret(ctx context.Context, name Name, input []byte) error {
	// Podman 5.4 rejects empty data and data at or above 512000 bytes.
	if name.value == "" || len(input) == 0 || len(input) >= 512000 {
		return invalid()
	}
	_, e := c.run(ctx, []string{"secret", "create", name.value, "-"}, input, true)
	return e
}
func (c *Client) SecretNames(ctx context.Context) ([]Name, error) {
	// Unlike image ls, Podman 5.4 secret ls treats "json" as a literal template.
	r, e := c.run(ctx, []string{"secret", "ls", "--format", "{{.Name}}"}, nil, false)
	if e != nil {
		return nil, e
	}
	names := make([]Name, 0)
	if r.Stdout == "" {
		return names, nil
	}
	for _, line := range strings.Split(strings.TrimSuffix(r.Stdout, "\n"), "\n") {
		n, e := ParseName(line)
		if e != nil {
			return nil, malformed()
		}
		names = append(names, n)
	}
	return names, nil
}
func (c *Client) ContainerState(ctx context.Context, name Name) (ContainerState, error) {
	if name.value == "" {
		return ContainerState{}, invalid()
	}
	// Probe existence first; Podman inspect uses exit 125 for both missing objects and runtime failures.
	found, e := c.exists(ctx, []string{"container", "exists", name.value})
	if e != nil {
		return ContainerState{}, e
	}
	if !found {
		return ContainerState{}, &localexec.Error{Kind: localexec.NotFound}
	}
	r, e := c.run(ctx, []string{"container", "inspect", name.value}, nil, false)
	if e != nil {
		return ContainerState{}, e
	}
	var rows []struct{ State *ContainerState }
	if json.Unmarshal([]byte(r.Stdout), &rows) != nil || len(rows) != 1 || rows[0].State == nil || rows[0].State.Status == "" {
		return ContainerState{}, malformed()
	}
	return *rows[0].State, nil
}

const ociManifest = "application/vnd.oci.image.manifest.v1+json"
const dockerManifest = "application/vnd.docker.distribution.manifest.v2+json"

type localImage struct {
	ID           string
	Digest       string
	RepoDigests  []string
	Os           string
	Architecture string
	ManifestType string
}

func (i Image) repository() string {
	repo, _, _ := strings.Cut(i.value, "@")
	if colon := strings.LastIndexByte(repo, ':'); colon > strings.LastIndexByte(repo, '/') {
		repo = repo[:colon]
	}
	domain, path, _ := strings.Cut(repo, "/")
	if domain != "localhost" && !strings.ContainsAny(domain, ".:") {
		domain, path = "docker.io", repo
	}
	if domain == "index.docker.io" {
		domain = "docker.io"
	}
	if domain == "docker.io" && !strings.ContainsRune(path, '/') {
		path = "library/" + path
	}
	return domain + "/" + path
}
func (i Image) digest() string { return i.value[strings.LastIndexByte(i.value, '@')+1:] }
func (row localImage) associates(image Image) bool {
	ref := image.repository() + "@" + image.digest()
	for _, associated := range row.RepoDigests {
		candidate, err := ParseImage(associated)
		if err == nil && candidate.repository()+"@"+candidate.digest() == ref {
			return true
		}
	}
	return false
}
func (c *Client) localImage(ctx context.Context, image Image) (localImage, error) {
	r, e := c.run(ctx, []string{"image", "inspect", image.value}, nil, false)
	if e != nil {
		return localImage{}, e
	}
	var rows []localImage
	if json.Unmarshal([]byte(r.Stdout), &rows) != nil || len(rows) != 1 {
		return localImage{}, malformed()
	}
	row := rows[0]
	if !digestPattern.MatchString("sha256:"+row.ID) || !digestPattern.MatchString(row.Digest) || row.Os == "" || row.Architecture == "" || !row.associates(image) {
		return localImage{}, malformed()
	}
	if row.ManifestType != ociManifest && row.ManifestType != dockerManifest {
		return localImage{}, malformed()
	}
	return row, nil
}

// Inspect observes local image metadata and the pinned registry manifest. It
// binds candidate platform manifests back to the same stored image ID, rather
// than guessing from architecture or the store's primary lookup digest.
func (c *Client) Inspect(ctx context.Context, image Image) (ImageInfo, error) {
	if image.value == "" {
		return ImageInfo{}, invalid()
	}
	found, e := c.ImageExists(ctx, image)
	if e != nil {
		return ImageInfo{}, e
	}
	if !found {
		return ImageInfo{}, &localexec.Error{Kind: localexec.NotFound}
	}
	row, e := c.localImage(ctx, image)
	if e != nil {
		return ImageInfo{}, e
	}
	info := ImageInfo{ImageID: row.ID, IndexDigest: image.digest(), Platform: Platform{OS: row.Os, Architecture: row.Architecture}}
	r, e := c.run(ctx, []string{"manifest", "inspect", image.value}, nil, false)
	if e != nil {
		// Podman 5.4's list-only parser rejects OCI single manifests. Only its
		// captured capability refusal permits using the already-verified local pin.
		var re *localexec.Error
		unsupported := strings.HasPrefix(r.Stderr, "Error: parsing manifest blob ") && strings.HasSuffix(strings.TrimSpace(r.Stderr), `as a "application/vnd.oci.image.manifest.v1+json": Treating single images as manifest lists is not implemented`)
		if row.ManifestType == ociManifest && !r.Truncated && errors.As(e, &re) && re.Kind == localexec.Failed && re.ExitCode == 125 && unsupported {
			info.ManifestDigest = image.digest()
			return info, nil
		}
		return ImageInfo{}, e
	}
	var manifest struct {
		MediaType string
		Manifests []struct {
			Digest   string
			Platform Platform
		}
	}
	if json.Unmarshal([]byte(r.Stdout), &manifest) != nil {
		return ImageInfo{}, malformed()
	}
	switch manifest.MediaType {
	case "application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json":
		for _, m := range manifest.Manifests {
			if m.Platform.OS != row.Os || m.Platform.Architecture != row.Architecture {
				continue
			}
			candidate, e := ParseImage(image.repository() + "@" + m.Digest)
			if e != nil {
				return ImageInfo{}, malformed()
			}
			if !row.associates(candidate) {
				continue
			}
			selected, e := c.localImage(ctx, candidate)
			if e != nil {
				return ImageInfo{}, e
			}
			if selected.ID != row.ID || selected.Os != row.Os || selected.Architecture != row.Architecture {
				continue
			}
			if info.ManifestDigest != "" {
				return ImageInfo{}, malformed()
			}
			info.ManifestDigest = m.Digest
			info.Platform = m.Platform
		}
	case dockerManifest:
		// The schema-2 single-image result loses config/layers when Podman converts
		// it to ManifestListData. The supported local lookup verifies this pin.
		if row.ManifestType != dockerManifest || len(manifest.Manifests) != 0 {
			return ImageInfo{}, malformed()
		}
		info.ManifestDigest = image.digest()
	default:
		return ImageInfo{}, malformed()
	}
	if info.ManifestDigest == "" {
		return ImageInfo{}, malformed()
	}
	return info, nil
}

var _ Adapter = (*Client)(nil)

// RunningContainerImage binds a running systemd container to a verified image pin.
func (c *Client) RunningContainerImage(ctx context.Context, name Name, unit string, image Image) (ImageInfo, error) {
	if name.value == "" || unit == "" || image.value == "" {
		return ImageInfo{}, invalid()
	}
	found, err := c.exists(ctx, []string{"container", "exists", name.value})
	if err != nil {
		return ImageInfo{}, err
	}
	if !found {
		return ImageInfo{}, &localexec.Error{Kind: localexec.NotFound}
	}
	r, err := c.run(ctx, []string{"container", "inspect", name.value}, nil, false)
	if err != nil {
		return ImageInfo{}, err
	}
	var rows []struct {
		Name   string
		Image  string
		State  *ContainerState
		Config struct{ Labels map[string]string }
	}
	if json.Unmarshal([]byte(r.Stdout), &rows) != nil || len(rows) != 1 {
		return ImageInfo{}, malformed()
	}
	row := rows[0]
	if row.Name != name.value || row.State == nil || !row.State.Running || row.State.Status != "running" || row.Config.Labels["PODMAN_SYSTEMD_UNIT"] != unit || !digestPattern.MatchString("sha256:"+row.Image) {
		return ImageInfo{}, malformed()
	}
	info, err := c.Inspect(ctx, image)
	if err != nil {
		return ImageInfo{}, err
	}
	if row.Image != info.ImageID {
		return ImageInfo{}, malformed()
	}
	return info, nil
}
