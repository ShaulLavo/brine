package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

var ErrProfile = errors.New("cdn cloudflare: invalid provider profile")
var ErrCredential = errors.New("cdn cloudflare: local credential unavailable")

var nativeID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var workerPrefix = regexp.MustCompile(`^brine-cdn-[a-z0-9-]{1,24}$`)
var credentialName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
var bearerToken = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+={0,4}$`)

func validToken(token string) bool {
	return len(token) > 0 && len(token) <= 4096 && bearerToken.MatchString(token)
}

type profileDocument struct {
	SchemaVersion int    `json:"schema_version"`
	AccountID     string `json:"account_id"`
	ZoneID        string `json:"zone_id"`
	WorkerPrefix  string `json:"worker_prefix"`
	RoutePattern  string `json:"route_pattern"`
	DNSRecordID   string `json:"dns_record_id"`
	CredentialRef string `json:"credential_ref,omitempty"`
}

// Profile contains only nonsecret provider configuration. The host pins its hash.
type Profile struct{ document profileDocument }

func ParseProfile(raw []byte) (Profile, error) {
	if len(raw) > 16<<10 {
		return Profile{}, ErrProfile
	}
	_, err := strictjson.Object(raw, "schema_version", "account_id", "zone_id", "worker_prefix", "route_pattern", "dns_record_id", "credential_ref")
	if err != nil {
		return Profile{}, ErrProfile
	}
	var document profileDocument
	if json.Unmarshal(raw, &document) != nil || !validProfile(document) {
		return Profile{}, ErrProfile
	}
	return Profile{document}, nil
}

func validProfile(p profileDocument) bool {
	if p.SchemaVersion != 1 || !nativeID.MatchString(p.AccountID) || !nativeID.MatchString(p.ZoneID) || !nativeID.MatchString(p.DNSRecordID) {
		return false
	}
	if !workerPrefix.MatchString(p.WorkerPrefix) || !credentialName.MatchString(p.CredentialRef) || strings.Count(p.RoutePattern, "*") != 1 || !strings.HasSuffix(p.RoutePattern, "/*") {
		return false
	}
	u, err := url.Parse(strings.TrimSuffix(p.RoutePattern, "*"))
	return err == nil && u.Scheme == "https" && u.Host == u.Hostname() && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.EscapedPath() == u.Path && !strings.ContainsAny(u.Path, "%\\")
}

func (p Profile) Fingerprint() cdn.Fingerprint {
	document := p.document
	document.CredentialRef = ""
	raw, _ := json.Marshal(document)
	return cdn.Hash(raw)
}

func (p Profile) Scope(reference cdn.ProfileRef) (cdn.ProfileScope, error) {
	scope := cdn.ProfileScope{Reference: reference, Driver: "cloudflare", Fingerprint: p.Fingerprint()}
	if !validProfile(p.document) || !scope.Valid() {
		return cdn.ProfileScope{}, ErrProfile
	}
	return scope, nil
}

type TokenSource func(context.Context, string) (string, error)

// FileToken reads one explicit local file; no ambient account login is consulted.
func FileToken(reference, path string) TokenSource {
	return func(ctx context.Context, requested string) (string, error) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if requested != reference || !credentialName.MatchString(reference) {
			return "", ErrCredential
		}
		return readToken(path)
	}
}

func readToken(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() > 4096 {
		return "", ErrCredential
	}
	file, err := os.Open(path)
	if err != nil {
		return "", ErrCredential
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(before, actual) || actual.Mode().Perm() != 0600 {
		return "", ErrCredential
	}
	raw, err := io.ReadAll(io.LimitReader(file, 4097))
	token := strings.TrimSpace(string(raw))
	if err != nil || len(raw) > 4096 || !validToken(token) {
		return "", ErrCredential
	}
	return token, nil
}
