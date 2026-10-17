package cdn

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

const ImmutableTTL = 31536000

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
var appPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var hashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var extensionPattern = regexp.MustCompile(`^[a-z0-9]{1,10}$`)
var prefixPattern = regexp.MustCompile(`^/(?:[A-Za-z0-9._~-]+/)+$`)

func Hash(data []byte) Fingerprint {
	sum := sha256.Sum256(data)
	return Fingerprint("sha256:" + hex.EncodeToString(sum[:]))
}

func ParseBinding(data []byte) (Binding, error) {
	var binding Binding
	if len(data) > 64<<10 {
		return binding, ErrInvalid
	}
	fields, err := strictjson.Object(data, "reference", "app", "target", "hostname", "origin", "cache", "allowed_edges", "traffic", "traffic_authority")
	if err != nil || !exactBindingFields(fields) {
		return Binding{}, ErrInvalid
	}
	if json.Unmarshal(data, &binding) != nil || binding.Validate() != nil {
		return Binding{}, ErrInvalid
	}
	return binding, nil
}

func exactBindingFields(fields map[string]json.RawMessage) bool {
	for key, names := range map[string][]string{"origin": {"address", "host", "tls_name"}, "cache": {"prefix", "extensions", "ttl_seconds"}, "traffic": {"reference", "driver", "fingerprint"}, "traffic_authority": {"resource_id", "hostname", "before"}} {
		if _, err := strictjson.Object(fields[key], names...); err != nil {
			return false
		}
	}
	profiles, err := strictjson.Value[[]json.RawMessage](fields["allowed_edges"])
	if err != nil {
		return false
	}
	for _, profile := range profiles {
		if _, err := strictjson.Object(profile, "reference", "driver", "fingerprint"); err != nil {
			return false
		}
	}
	return true
}

func (b Binding) Validate() error {
	if !namePattern.MatchString(b.Reference) || !appPattern.MatchString(b.App) || !hashPattern.MatchString(string(b.Target)) || !hostname(b.Hostname) {
		return ErrInvalid
	}
	u, err := url.Parse(b.Origin.Address)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return ErrInvalid
	}
	if !hostname(b.Origin.Host) || !hostname(b.Origin.TLSName) || b.Cache.Validate() != nil || !b.Traffic.Valid() || len(b.AllowedEdges) == 0 || len(b.AllowedEdges) > 16 {
		return ErrInvalid
	}
	if b.TrafficAuthority.ResourceID == "" || len(b.TrafficAuthority.ResourceID) > 256 || b.TrafficAuthority.Hostname != b.Hostname || !hashPattern.MatchString(string(b.TrafficAuthority.Before)) {
		return ErrInvalid
	}
	seen := map[ProfileRef]bool{}
	for _, profile := range b.AllowedEdges {
		if !profile.Valid() || seen[profile.Reference] {
			return ErrInvalid
		}
		seen[profile.Reference] = true
	}
	return nil
}

func (p ProfileScope) Valid() bool {
	return namePattern.MatchString(string(p.Reference)) && namePattern.MatchString(p.Driver) && hashPattern.MatchString(string(p.Fingerprint))
}

func hostname(value string) bool {
	if value != strings.ToLower(value) || len(value) > 253 || !strings.Contains(value, ".") || strings.ContainsAny(value, "/:*@% \\?#") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || strings.Trim(label, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	return net.ParseIP(value) == nil
}

func (p CachePolicy) Validate() error {
	if len(p.Prefix) > 256 || !prefixPattern.MatchString(p.Prefix) || path.Clean(p.Prefix)+"/" != p.Prefix || p.TTLSeconds != ImmutableTTL || len(p.Extensions) == 0 || len(p.Extensions) > 16 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, extension := range p.Extensions {
		if !extensionPattern.MatchString(extension) || seen[extension] {
			return ErrInvalid
		}
		seen[extension] = true
	}
	return nil
}

func (b Binding) CanonicalBytes() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	b.Cache.Extensions = slices.Clone(b.Cache.Extensions)
	slices.Sort(b.Cache.Extensions)
	b.AllowedEdges = slices.Clone(b.AllowedEdges)
	slices.SortFunc(b.AllowedEdges, func(a, c ProfileScope) int { return strings.Compare(string(a.Reference), string(c.Reference)) })
	return json.Marshal(b)
}

func (p CachePolicy) AssetDigest(request *http.Request, host string) (Fingerprint, bool) {
	if request == nil || request.URL == nil || request.Host != host || request.Method != http.MethodGet && request.Method != http.MethodHead || request.URL.RawQuery != "" || request.URL.ForceQuery {
		return "", false
	}
	if request.URL.EscapedPath() != request.URL.Path || len(request.Header.Values("Authorization")) != 0 || len(request.Header.Values("Cookie")) != 0 || len(request.Header.Values("Range")) != 0 {
		return "", false
	}
	if !strings.HasPrefix(request.URL.Path, p.Prefix) {
		return "", false
	}
	file := strings.TrimPrefix(request.URL.Path, p.Prefix)
	base, extension, found := strings.Cut(file, ".")
	digest := Fingerprint("sha256:" + base)
	return digest, found && hashPattern.MatchString(string(digest)) && slices.Contains(p.Extensions, extension)
}

func NewAssetSample(policy CachePolicy, assetPath string) (AssetSample, error) {
	if policy.Validate() != nil || !strings.HasPrefix(assetPath, "/") {
		return AssetSample{}, ErrInvalid
	}
	u, err := url.ParseRequestURI(assetPath)
	if err != nil || u.IsAbs() || u.Host != "" {
		return AssetSample{}, ErrInvalid
	}
	request := &http.Request{Method: http.MethodGet, Host: "qualification.invalid", URL: u}
	digest, ok := policy.AssetDigest(request, request.Host)
	if !ok {
		return AssetSample{}, ErrInvalid
	}
	return AssetSample{Path: assetPath, Digest: digest}, nil
}

func (q Qualification) Validate() error {
	sample, err := NewAssetSample(q.Attachment.Binding.Cache, q.Sample.Path)
	if err != nil || sample != q.Sample {
		return ErrInvalid
	}
	return q.Attachment.Binding.Validate()
}
