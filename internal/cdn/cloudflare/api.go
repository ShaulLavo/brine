package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type Permission string

const (
	ReadWorkers  Permission = "workers_scripts_read"
	WriteWorkers Permission = "workers_scripts_write"
	ReadRoutes   Permission = "workers_routes_read"
	WriteRoutes  Permission = "workers_routes_write"
	ReadDNS      Permission = "dns_records_read"
	WriteDNS     Permission = "dns_records_write"
)

type ErrorKind string

const (
	PermissionDenied  ErrorKind = "permission_denied"
	OwnershipConflict ErrorKind = "ownership_conflict"
	NotFound          ErrorKind = "not_found"
	InvalidResponse   ErrorKind = "invalid_response"
	Unavailable       ErrorKind = "unavailable"
	WriteUnknown      ErrorKind = "write_unknown"
)

type APIError struct {
	Kind       ErrorKind
	Permission Permission
	Status     int
}

func (e *APIError) Error() string {
	return "cdn cloudflare: " + string(e.Kind) + " (" + string(e.Permission) + ")"
}

const responseLimit = 2 << 20

type nativeClient struct {
	profile Profile
	tokens  TokenSource
	http    *http.Client
	base    string
}

type reply struct {
	status  int
	headers http.Header
	body    []byte
}

type envelope struct {
	Success    bool            `json:"success"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		TotalCount int `json:"total_count"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

func newClient(profile Profile, tokens TokenSource) nativeClient {
	return nativeClient{profile: profile, tokens: tokens, base: "https://api.cloudflare.com/client/v4",
		http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func (c nativeClient) call(ctx context.Context, method, path, contentType string, body io.Reader, permission Permission) (reply, error) {
	if c.tokens == nil {
		return reply{}, ErrCredential
	}
	token, err := c.tokens(ctx, c.profile.document.CredentialRef)
	if err != nil || !validToken(token) {
		return reply{}, ErrCredential
	}
	request, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return reply{}, ErrProfile
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return reply{}, requestFailure(method, permission, 0)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil || len(raw) > responseLimit {
		return reply{}, requestFailure(method, permission, response.StatusCode)
	}
	out := reply{status: response.StatusCode, headers: response.Header, body: raw}
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return out, &APIError{Kind: PermissionDenied, Permission: permission, Status: response.StatusCode}
	}
	if response.StatusCode == http.StatusNotFound && method == http.MethodGet {
		return out, &APIError{Kind: NotFound, Permission: permission, Status: response.StatusCode}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return out, requestFailure(method, permission, response.StatusCode)
	}
	return out, nil
}

func requestFailure(method string, permission Permission, status int) error {
	kind := Unavailable
	if method != http.MethodGet {
		kind = WriteUnknown
	}
	return &APIError{Kind: kind, Permission: permission, Status: status}
}

func decodeEnvelope(response reply, permission Permission, output any) error {
	var parsed envelope
	if json.Unmarshal(response.body, &parsed) != nil || !parsed.Success || len(parsed.Result) == 0 || string(parsed.Result) == "null" || json.Unmarshal(parsed.Result, output) != nil {
		return &APIError{Kind: InvalidResponse, Permission: permission, Status: response.status}
	}
	return nil
}

func (c nativeClient) get(ctx context.Context, path string, permission Permission, output any) error {
	response, err := c.call(ctx, http.MethodGet, path, "", nil, permission)
	if err != nil {
		return err
	}
	return decodeEnvelope(response, permission, output)
}

func isMissing(err error) bool {
	var problem *APIError
	return errors.As(err, &problem) && problem.Kind == NotFound
}

func nativePath(parts ...string) string { return "/" + strings.Join(parts, "/") }
