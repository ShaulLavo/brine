package cdnoperator

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/cdn"
)

const originBodyLimit = 64 << 20

// VerifyOrigin checks the bound TLS origin before the hostname traffic changes.
func VerifyOrigin(ctx context.Context, q cdn.Qualification) (cdn.Fingerprint, error) {
	if q.Validate() != nil {
		return "", cdn.ErrInvalid
	}
	origin := q.Attachment.Binding.Origin
	transport := &http.Transport{ForceAttemptHTTP2: true, DisableCompression: true,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		TLSClientConfig: &tls.Config{ServerName: origin.TLSName, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return VerifyOriginWithClient(ctx, q, client)
}

func VerifyOriginWithClient(ctx context.Context, q cdn.Qualification, client *http.Client) (cdn.Fingerprint, error) {
	if q.Validate() != nil || client == nil {
		return "", cdn.ErrInvalid
	}
	origin := q.Attachment.Binding.Origin
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(origin.Address, "/")+q.Sample.Path, nil)
	if err != nil {
		return "", cdn.ErrInvalid
	}
	request.Host = origin.Host
	request.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(request)
	if err != nil {
		return "", cdn.ErrConflict
	}
	defer func() { _ = response.Body.Close() }()
	if !publicOriginResponse(response) {
		return "", cdn.ErrConflict
	}
	digest := sha256.New()
	length, err := io.Copy(digest, io.LimitReader(response.Body, originBodyLimit+1))
	actual := cdn.Fingerprint("sha256:" + hex.EncodeToString(digest.Sum(nil)))
	if err != nil || length > originBodyLimit || response.ContentLength != length || actual != q.Sample.Digest {
		return "", cdn.ErrConflict
	}
	raw, _ := json.Marshal(struct {
		Origin cdn.Origin
		Sample cdn.AssetSample
		Length int64
		Type   string
	}{origin, q.Sample, length, response.Header.Get("Content-Type")})
	return cdn.Hash(raw), nil
}

func publicOriginResponse(response *http.Response) bool {
	cache := strings.ToLower(response.Header.Get("Cache-Control"))
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	encoding := strings.ToLower(response.Header.Get("Content-Encoding"))
	return response.StatusCode == http.StatusOK && response.ContentLength >= 0 && contentType != "" && !strings.HasPrefix(contentType, "text/html") && !strings.HasPrefix(contentType, "application/xhtml+xml") && !strings.Contains(cache, "private") && !strings.Contains(cache, "no-store") && len(response.Header.Values("Set-Cookie")) == 0 && (encoding == "" || encoding == "identity")
}
