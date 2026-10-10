package restore

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// VerifyRemoteAccess proves the freshly activated credential can read the exact
// committed epoch prefix. It never syncs, uploads, retries or reads a live database.
// An empty listing is valid access evidence, not evidence that a backup exists.
func VerifyRemoteAccess(ctx context.Context, destination Destination, credentials Credentials) error {
	return verifyRemoteAccess(ctx, destination, credentials, false)
}
func verifyRemoteAccess(ctx context.Context, d Destination, c Credentials, allowHTTP bool) error {
	const budget = 30 * time.Second
	if err := validateDestination(d, allowHTTP); err != nil {
		return err
	}
	if err := validateCredentials(c, time.Now().UTC(), budget); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	u, _ := url.Parse(d.Endpoint)
	if d.PathStyle {
		u.Path = "/" + d.Bucket
	} else {
		u.Host = d.Bucket + "." + u.Host
		u.Path = "/"
	}
	prefix := strings.TrimSuffix(d.Prefix, "/") + "/"
	u.RawQuery = url.Values{"list-type": {"2"}, "max-keys": {"1"}, "prefix": {prefix}}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return refuse("remote_access_request")
	}
	signGET(request, d.Region, c, time.Now().UTC())
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return refuse("remote_access_failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return refuse("remote_access_refused")
	}
	const limit = 64 << 10
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(raw) > limit {
		return refuse("remote_access_response")
	}
	var listing struct {
		XMLName  xml.Name `xml:"ListBucketResult"`
		Name     string   `xml:"Name"`
		Prefix   string   `xml:"Prefix"`
		MaxKeys  int      `xml:"MaxKeys"`
		KeyCount int      `xml:"KeyCount"`
		Contents []struct {
			Key string `xml:"Key"`
		} `xml:"Contents"`
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&listing) != nil || listing.Name != d.Bucket || listing.Prefix != prefix || listing.MaxKeys != 1 || listing.KeyCount < 0 || listing.KeyCount > 1 || len(listing.Contents) != listing.KeyCount {
		return refuse("remote_access_response")
	}
	// Reject a second document, trailing garbage and truncated XML.
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return refuse("remote_access_response")
		}
		text, ok := token.(xml.CharData)
		if !ok || len(bytes.TrimSpace(text)) != 0 {
			return refuse("remote_access_response")
		}
	}
	for _, object := range listing.Contents {
		if !strings.HasPrefix(object.Key, prefix) {
			return refuse("remote_access_scope")
		}
	}
	return nil
}
