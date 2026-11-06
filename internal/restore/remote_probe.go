package restore

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"time"
)

// RemoteState is a closed observation class, never a provider message.
type RemoteState string

const (
	RemoteReachable           RemoteState = "reachable"
	RemoteEmpty               RemoteState = "empty"
	RemoteAuthRefused         RemoteState = "auth_refused"
	RemoteCredentialsRejected RemoteState = "credentials_rejected"
	RemoteDestinationMissing  RemoteState = "destination_missing"
	RemoteStorageUnavailable  RemoteState = "storage_unavailable"
	RemoteNetworkUnavailable  RemoteState = "network_unavailable"
	RemoteResponseInvalid     RemoteState = "response_invalid"
	RemoteNotChecked          RemoteState = "not_checked"
)

type RemoteProbe struct {
	State      RemoteState `json:"state"`
	ObservedAt time.Time   `json:"observed_at"`
}

// ProbeRemote observes the exact epoch with one signed, read-only listing and a
// 15-second budget. Unusable credentials or destinations are never sent. Neither
// reachable nor empty proves a recoverable backup or admits a write.
func ProbeRemote(ctx context.Context, d Destination, c Credentials) RemoteProbe {
	return probeRemote(ctx, d, c, false)
}

func probeRemote(ctx context.Context, d Destination, c Credentials, allowHTTP bool) RemoteProbe {
	state := probeRemoteState(ctx, d, c, allowHTTP)
	return RemoteProbe{State: state, ObservedAt: time.Now().UTC()}
}

func probeRemoteState(ctx context.Context, d Destination, c Credentials, allowHTTP bool) RemoteState {
	const budget = 15 * time.Second
	if validateDestination(d, allowHTTP) != nil || validateCredentials(c, time.Now().UTC(), budget) != nil {
		return RemoteNotChecked
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	request, err := remoteListingRequest(ctx, d, c)
	if err != nil {
		return RemoteNotChecked
	}
	client := newS3ReadClient()
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return RemoteNetworkUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	limit := int64(4 << 10)
	if response.StatusCode == http.StatusOK {
		limit = 64 << 10
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return RemoteNetworkUnavailable
	}
	if int64(len(raw)) > limit {
		return RemoteResponseInvalid
	}
	if response.StatusCode != http.StatusOK {
		return remoteFailureState(response.StatusCode, raw)
	}
	count, err := remoteListingCount(raw, d)
	if err != nil {
		return RemoteResponseInvalid
	}
	if count == 0 {
		return RemoteEmpty
	}
	return RemoteReachable
}

func remoteFailureState(status int, raw []byte) RemoteState {
	var failure struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
	}
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	valid := decoder.Decode(&failure) == nil && xmlEnd(decoder) == nil
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		if valid && (failure.Code == "ExpiredToken" || failure.Code == "InvalidAccessKeyId" || failure.Code == "SignatureDoesNotMatch") {
			return RemoteCredentialsRejected
		}
		return RemoteAuthRefused
	case status == http.StatusNotFound && valid && failure.Code == "NoSuchBucket":
		return RemoteDestinationMissing
	case status == http.StatusTooManyRequests || status >= 500 && status <= 599:
		return RemoteStorageUnavailable
	default:
		return RemoteResponseInvalid
	}
}
