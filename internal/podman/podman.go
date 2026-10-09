// Package podman exposes only the runtime operations needed by deployment.
package podman

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type Image struct{ value string }

func (i Image) String() string { return i.value }

type Name struct{ value string }

func (n Name) String() string { return n.value }

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,252}$`)
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

// Inspect observes local image metadata and the pinned registry manifest. The
// latter may contact the registry, but never pulls or changes the image store.
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
	r, e := c.run(ctx, []string{"image", "inspect", image.value}, nil, false)
	if e != nil {
		return ImageInfo{}, e
	}
	var rows []struct{ Digest, Os, Architecture, Variant, ManifestType string }
	if json.Unmarshal([]byte(r.Stdout), &rows) != nil || len(rows) != 1 {
		return ImageInfo{}, malformed()
	}
	row := rows[0]
	pin := image.value[strings.LastIndexByte(image.value, '@')+1:]
	if row.Digest != pin || row.Os == "" || row.Architecture == "" {
		return ImageInfo{}, malformed()
	}
	info := ImageInfo{IndexDigest: pin, Platform: Platform{OS: row.Os, Architecture: row.Architecture, Variant: row.Variant}}
	r, e = c.run(ctx, []string{"manifest", "inspect", image.value}, nil, false)
	if e != nil {
		return ImageInfo{}, e
	}
	var manifest struct {
		MediaType string
		Manifests []struct {
			Digest   string
			Platform Platform
		}
		Config *json.RawMessage
	}
	if json.Unmarshal([]byte(r.Stdout), &manifest) != nil {
		return ImageInfo{}, malformed()
	}
	switch manifest.MediaType {
	case "application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json":
		for _, m := range manifest.Manifests {
			if m.Platform.OS == info.Platform.OS && m.Platform.Architecture == info.Platform.Architecture && (row.Variant == "" || m.Platform.Variant == row.Variant) {
				if info.ManifestDigest != "" || !digestPattern.MatchString(m.Digest) {
					return ImageInfo{}, malformed()
				}
				info.ManifestDigest = m.Digest
				info.Platform = m.Platform
			}
		}
	case "application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json":
		if manifest.Config == nil {
			return ImageInfo{}, malformed()
		}
		info.ManifestDigest = pin
	default:
		return ImageInfo{}, malformed()
	}
	if info.ManifestDigest == "" {
		return ImageInfo{}, malformed()
	}
	return info, nil
}

var _ Adapter = (*Client)(nil)
