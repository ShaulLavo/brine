package cloudflare

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/cdn"
)

const proofBodyLimit = 64 << 20

type proofRead struct {
	Digest      cdn.Fingerprint
	Length      int64
	ContentType string
	Cache       string
}

func (d *Adapter) Probe(ctx context.Context, q cdn.Qualification) (cdn.PublicProof, error) {
	if d.traffic || q.Validate() != nil || d.admit(q.Attachment) != nil {
		return cdn.PublicProof{}, cdn.ErrInvalid
	}
	a := q.Attachment
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second, DisableCompression: true,
		TLSClientConfig: &tls.Config{ServerName: a.Binding.Origin.TLSName, MinVersion: tls.VersionTLS12}}
	origin := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: noRedirect}
	defer transport.CloseIdleConnections()
	edgeTransport := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true, DisableCompression: true,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second}
	edge := &http.Client{Transport: edgeTransport, Timeout: 15 * time.Second, CheckRedirect: noRedirect}
	defer edgeTransport.CloseIdleConnections()
	originRead, err := readProof(ctx, origin, strings.TrimSuffix(a.Binding.Origin.Address, "/")+q.Sample.Path, a.Binding.Origin.Host)
	if err != nil || originRead.Digest != q.Sample.Digest {
		return cdn.PublicProof{}, cdn.ErrConflict
	}
	url := "https://" + a.Binding.Hostname + q.Sample.Path
	first, err := readProof(ctx, edge, url, "")
	if err != nil {
		return cdn.PublicProof{}, err
	}
	second, err := readProof(ctx, edge, url, "")
	if err != nil {
		return cdn.PublicProof{}, err
	}
	return d.publicProof(q, originRead, first, second)
}

func (d *Adapter) publicProof(q cdn.Qualification, originRead, first, second proofRead) (cdn.PublicProof, error) {
	a := q.Attachment
	matched := first.Digest == q.Sample.Digest && second.Digest == q.Sample.Digest
	if !matched || originRead.Digest != q.Sample.Digest {
		return cdn.PublicProof{}, cdn.ErrConflict
	}
	direct := a.Mode == cdn.Direct
	if direct && (first.Cache != "" || second.Cache != "") {
		return cdn.PublicProof{}, cdn.ErrConflict
	}
	if !direct && (a.Mode != cdn.Enabled || second.Cache != "HIT") {
		return cdn.PublicProof{}, cdn.ErrConflict
	}
	evidence := fingerprint(struct {
		Source                string
		Origin, First, Second proofRead
	}{"operator-http-full-get-v1", originRead, first, second})
	return cdn.PublicProof{Method: cdn.OperatorHTTP, Sample: q.Sample, Source: d.scope, ObservedAt: time.Now().UTC(), Revision: a.Revision,
		OriginHealthy: true, EdgeHealthy: !direct, FullGETs: 2, HashesVerified: true, LaterHit: !direct, Evidence: evidence}, nil
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func readProof(ctx context.Context, client *http.Client, url, host string) (proofRead, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return proofRead{}, cdn.ErrInvalid
	}
	request.Host = host
	request.Header.Set("Cache-Control", "no-cache, no-store")
	request.Header.Set("Pragma", "no-cache")
	request.Header.Set("Accept-Encoding", "identity")
	response, err := client.Do(request)
	if err != nil {
		return proofRead{}, cdn.ErrConflict
	}
	defer func() { _ = response.Body.Close() }()
	encoding := response.Header.Get("Content-Encoding")
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") == "" || encoding != "" && encoding != "identity" {
		return proofRead{}, cdn.ErrConflict
	}
	digest := sha256.New()
	length, err := io.Copy(digest, io.LimitReader(response.Body, proofBodyLimit+1))
	if err != nil || length > proofBodyLimit || response.ContentLength != length {
		return proofRead{}, cdn.ErrConflict
	}
	return proofRead{Digest: cdn.Fingerprint("sha256:" + hex.EncodeToString(digest.Sum(nil))), Length: length,
		ContentType: response.Header.Get("Content-Type"), Cache: response.Header.Get("X-Brine-CDN-Cache")}, nil
}
