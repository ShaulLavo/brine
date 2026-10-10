package restore

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func validateDestination(d Destination, allowHTTP bool) error {
	u, err := url.Parse(d.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return refuse("invalid_destination")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !allowHTTP || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return refuse("destination_requires_https")
		}
	}
	if len(d.Bucket) < 3 || len(d.Bucket) > 63 || strings.ContainsAny(d.Bucket, "/\\:@") || !validKey(d.Bucket) || strings.Contains(d.Bucket, "_") || !token.MatchString(d.Region) || !validKey(d.Prefix) {
		return refuse("invalid_destination")
	}
	for _, r := range d.Bucket {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return refuse("invalid_destination")
		}
	}
	return nil
}

func downloadSnapshot(ctx context.Context, d Destination, c Credentials, s SnapshotSource, output string, allowHTTP bool) error {
	if err := validateDestination(d, allowHTTP); err != nil {
		return err
	}
	u, _ := url.Parse(d.Endpoint)
	if d.PathStyle {
		u.Path = "/" + d.Bucket + "/" + s.ObjectKey
	} else {
		u.Host = d.Bucket + "." + u.Host
		u.Path = "/" + s.ObjectKey
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return refuse("snapshot_request")
	}
	signGET(request, d.Region, c, time.Now().UTC())
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return refuse("snapshot_download_failed")
	}
	defer func() { _ = response.Body.Close() }() // Bounded read errors are handled below; closing HTTP input cannot alter recovered bytes.
	if response.StatusCode != http.StatusOK {
		return refuse("snapshot_http_status")
	}
	if response.ContentLength >= 0 && response.ContentLength != s.Size {
		return refuse("snapshot_size_mismatch")
	}
	// #nosec G304 -- Engine constructs output inside the exclusively created private operation directory; O_EXCL refuses replacement.
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return refuse("snapshot_output")
	}
	defer func() { _ = file.Close() }() // Failure-path cleanup only; the successful path checks Close below.
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response.Body, s.Size+1))
	if err != nil {
		return refuse("snapshot_download_failed")
	}
	if size != s.Size {
		return refuse("snapshot_size_mismatch")
	}
	if hex.EncodeToString(digest.Sum(nil)) != s.SHA256 {
		return refuse("snapshot_hash_mismatch")
	}
	if err := file.Sync(); err != nil {
		return refuse("snapshot_sync_failed")
	}
	if err := file.Close(); err != nil {
		return refuse("snapshot_output")
	}
	return nil
}

// The download boundary supports one typed GET, with no credential-chain fallback,
// redirects, retry, upload or provider-specific API.
func signGET(request *http.Request, region string, c Credentials, now time.Time) {
	signS3(request, region, c, now, nil)
}

func signS3(request *http.Request, region string, c Credentials, now time.Time, body []byte) {
	timestamp := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	payload := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(payload[:])
	request.Header.Set("X-Amz-Date", timestamp)
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)
	canonicalHeaders := "host:" + request.URL.Host + "\n"
	signedHeaders := "host"
	if value := request.Header.Get("If-None-Match"); value != "" {
		canonicalHeaders += "if-none-match:" + value + "\n"
		signedHeaders += ";if-none-match"
	}
	canonicalHeaders += "x-amz-content-sha256:" + payloadHash + "\nx-amz-date:" + timestamp + "\n"
	signedHeaders += ";x-amz-content-sha256;x-amz-date"
	if c.SessionToken != "" {
		request.Header.Set("X-Amz-Security-Token", c.SessionToken)
		canonicalHeaders += "x-amz-security-token:" + c.SessionToken + "\n"
		signedHeaders += ";x-amz-security-token"
	}
	canonicalQuery := strings.ReplaceAll(request.URL.Query().Encode(), "+", "%20")
	canonical := request.Method + "\n" + request.URL.EscapedPath() + "\n" + canonicalQuery + "\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash

	scope := date + "/" + region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + timestamp + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	key := hmacSHA([]byte("AWS4"+c.SecretKey), date)
	key = hmacSHA(key, region)
	key = hmacSHA(key, "s3")
	key = hmacSHA(key, "aws4_request")
	signature := hex.EncodeToString(hmacSHA(key, toSign))
	request.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", c.AccessKey, scope, signedHeaders, signature))
}
func hmacSHA(key []byte, value string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(value))
	return h.Sum(nil)
}
