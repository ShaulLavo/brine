package cdn

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
)

const ImmutableTTL = 31536000

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
var hashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var extensionPattern = regexp.MustCompile(`^[a-z0-9]{1,10}$`)

func Hash(data []byte) Fingerprint {
	sum := sha256.Sum256(data)
	return Fingerprint("sha256:" + hex.EncodeToString(sum[:]))
}

func ParseBinding(data []byte) (Binding, error) {
	var binding Binding
	if len(data) > 64<<10 {
		return binding, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&binding) != nil || decoder.Decode(new(any)) != io.EOF {
		return Binding{}, ErrInvalid
	}
	if binding.Validate() != nil {
		return Binding{}, ErrInvalid
	}
	return binding, nil
}

func (b Binding) Validate() error {
	if !namePattern.MatchString(b.Reference) || !namePattern.MatchString(b.App) || !hashPattern.MatchString(string(b.Target)) || !hostname(b.Hostname) {
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
	if p.Prefix == "/" || !strings.HasPrefix(p.Prefix, "/") || !strings.HasSuffix(p.Prefix, "/") || path.Clean(p.Prefix)+"/" != p.Prefix || strings.ContainsAny(p.Prefix, "%?#\\") || p.TTLSeconds != ImmutableTTL || len(p.Extensions) == 0 || len(p.Extensions) > 16 {
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
	if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" || request.Header.Get("Range") != "" {
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
