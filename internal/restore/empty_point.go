package restore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// CreateEmptySnapshot creates a staging database, never the live app database.
// The two conditional PUTs establish create-only capability using the same
// harmless empty bytes: the first must succeed, the second must return 412.
// Unsupported destinations are refused without a HEAD/PUT race or any overwrite
// of preexisting data. Unknown requests are never retried by this adapter.
func CreateEmptySnapshot(ctx context.Context, root string, b Binding, c Credentials, pointID string, budget time.Duration, allowHTTPForTests bool) (SnapshotSource, time.Time, error) {
	var source SnapshotSource
	if !token.MatchString(b.ID) || !token.MatchString(b.Epoch) || !token.MatchString(b.CredentialRef) || !token.MatchString(pointID) || budget <= 0 || budget > 30*time.Minute {
		return source, time.Time{}, refuse("invalid_empty_point")
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if err := validateDestination(b.Destination, allowHTTPForTests); err != nil {
		return source, time.Time{}, err
	}
	if err := validateCredentials(c, time.Now().UTC(), budget); err != nil {
		return source, time.Time{}, err
	}
	directory, err := newDirectory(root, pointID+"-empty")
	if err != nil {
		return source, time.Time{}, err
	}
	defer func() { _ = os.RemoveAll(directory) }()
	file := filepath.Join(directory, "empty.sqlite")
	db, err := sql.Open("sqlite", file)
	if err != nil {
		return source, time.Time{}, refuse("empty_snapshot")
	}
	_, execErr := db.ExecContext(ctx, "PRAGMA user_version=0")
	closeErr := db.Close()
	if execErr != nil || closeErr != nil {
		return source, time.Time{}, refuse("empty_snapshot")
	}
	if err = os.Chmod(file, 0600); err != nil {
		return source, time.Time{}, refuse("empty_snapshot")
	}
	raw, err := os.ReadFile(file) // #nosec G304 -- File is generated inside the newly created, symlink-free private staging directory.
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return source, time.Time{}, refuse("empty_snapshot")
	}
	sum := sha256.Sum256(raw)
	source = SnapshotSource{BindingID: b.ID, Epoch: b.Epoch, PointID: pointID, ObjectKey: b.Destination.Prefix + "/restore-points/" + pointID + "/snapshot.sqlite", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(raw))}
	u, err := url.Parse(b.Destination.Endpoint)
	if err != nil {
		return source, time.Time{}, refuse("invalid_destination")
	}
	if b.Destination.PathStyle {
		u.Path = "/" + b.Destination.Bucket + "/" + source.ObjectKey
	} else {
		u.Host = b.Destination.Bucket + "." + u.Host
		u.Path = "/" + source.ObjectKey
	}
	transport := &http.Transport{DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableCompression: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var uploadedAt time.Time
	for attempt := 0; attempt < 2; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(raw))
		if err != nil {
			return source, time.Time{}, refuse("snapshot_request")
		}
		request.Header.Set("If-None-Match", "*")
		signS3(request, b.Destination.Region, c, time.Now().UTC(), raw)
		response, err := client.Do(request)
		if err != nil {
			return source, time.Time{}, refuse("snapshot_upload_unknown")
		}
		code := response.StatusCode
		closeErr := response.Body.Close()
		if closeErr != nil {
			return source, time.Time{}, refuse("snapshot_upload_unknown")
		}
		if attempt == 0 && (code != http.StatusOK && code != http.StatusCreated && code != http.StatusNoContent) {
			return source, time.Time{}, refuse("snapshot_create_only_failed")
		}
		if attempt == 0 {
			uploadedAt = time.Now().UTC()
		}
		if attempt == 1 && code != http.StatusPreconditionFailed {
			return source, time.Time{}, refuse("snapshot_create_only_unsupported")
		}
	}
	return source, uploadedAt, nil
}

// EmptySchemaObserver accepts only a catalog with no application objects.
// Engine.Test supplies query_only, integrity_check and foreign_key_check.
type EmptySchemaObserver struct{}

func (EmptySchemaObserver) Observe(ctx context.Context, tx *sql.Tx) (SchemaObservation, error) {
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*'").Scan(&count); err != nil || count != 0 {
		return SchemaObservation{}, refuse("not_empty")
	}
	return SchemaObservation{State: VerifiedEmpty, Marker: "brine-empty-v1", CatalogSHA256: "4f53cda18c2baa0c0354bb5f9a3ecbe5ed12ab4d8e11ba873c2f11161202b945"}, nil
}
