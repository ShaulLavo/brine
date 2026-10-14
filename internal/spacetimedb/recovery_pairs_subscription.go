package spacetimedb

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
)

const confirmedSubscriptionProtocol = "v1.json.spacetimedb"

func readConfirmedSubscription(ctx context.Context, endpoint PrivateEndpoint, credential PublisherCredential, limits ObserverLimits, layout string) ([]recoveryPair, error) {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10, ResponseHeaderTimeout: limits.Timeout}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRecoveryPairs }}
	address := "ws" + strings.TrimPrefix(endpoint.base, "http") + "/v1/database/" + url.PathEscape(string(endpoint.database)) + "/subscribe?confirmed=true&compression=None"
	headers := http.Header{"Authorization": []string{"Bearer " + credential.bearer}}
	connection, response, err := websocket.Dial(ctx, address, &websocket.DialOptions{HTTPClient: client, HTTPHeader: headers, Subprotocols: []string{confirmedSubscriptionProtocol}})
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil {
		return nil, ErrRecoveryPairs
	}
	defer func() { _ = connection.CloseNow() }()
	if connection.Subprotocol() != confirmedSubscriptionProtocol {
		return nil, ErrRecoveryPairs
	}
	connection.SetReadLimit(MaxRecoveryPairsBytes)
	kind, raw, err := connection.Read(ctx)
	if err != nil || kind != websocket.MessageText || checkSubscriptionIdentity(raw, limits.Schema) != nil {
		return nil, ErrRecoveryPairs
	}
	request := []byte(`{"SubscribeMulti":{"query_strings":["SELECT * FROM recovery_left","SELECT * FROM recovery_right","SELECT * FROM schema_marker"],"request_id":1,"query_id":{"id":1}}}`)
	if connection.Write(ctx, websocket.MessageText, request) != nil {
		return nil, ErrRecoveryPairs
	}
	kind, raw, err = connection.Read(ctx)
	if err != nil || kind != websocket.MessageText {
		return nil, ErrRecoveryPairs
	}
	return decodeConfirmedSubscription(raw, limits.Schema, layout)
}

func checkSubscriptionIdentity(raw []byte, limits SchemaLimits) error {
	value, err := decodeDocument(raw, limits)
	name, body, variantErr := variant(value)
	if err != nil || variantErr != nil || name != "IdentityToken" {
		return ErrRecoveryPairs
	}
	identity, err := exactObject(body, "identity", "token", "connection_id")
	if err != nil {
		return ErrRecoveryPairs
	}
	if _, err := nativeIdentity(identity["identity"]); err != nil {
		return ErrRecoveryPairs
	}
	connection, err := exactObject(identity["connection_id"], "__connection_id__")
	if err != nil {
		return ErrRecoveryPairs
	}
	number, ok := connection["__connection_id__"].(json.Number)
	if !ok {
		return ErrRecoveryPairs
	}
	parsed, valid := new(big.Int).SetString(number.String(), 10)
	if !valid || parsed.Sign() < 0 || parsed.BitLen() > 128 || parsed.String() != number.String() {
		return ErrRecoveryPairs
	}
	token, ok := identity["token"].(string)
	if !ok || len(token) == 0 || len(token) > 16<<10 {
		return ErrRecoveryPairs
	}
	return nil
}

func decodeConfirmedSubscription(raw []byte, limits SchemaLimits, layout string) ([]recoveryPair, error) {
	if len(raw) > MaxRecoveryPairsBytes {
		return nil, ErrRecoveryPairs
	}
	value, err := decodeDocument(raw, limits)
	name, body, variantErr := variant(value)
	if err != nil || variantErr != nil || name != "SubscribeMultiApplied" {
		return nil, ErrRecoveryPairs
	}
	applied, err := exactObject(body, "request_id", "query_id", "total_host_execution_duration_micros", "update")
	if err != nil {
		return nil, ErrRecoveryPairs
	}
	request, err := integer(applied["request_id"])
	query, queryErr := exactObject(applied["query_id"], "id")
	if err != nil || request != 1 || queryErr != nil {
		return nil, ErrRecoveryPairs
	}
	queryID, err := integer(query["id"])
	_, durationErr := integer(applied["total_host_execution_duration_micros"])
	if err != nil || queryID != 1 || durationErr != nil {
		return nil, ErrRecoveryPairs
	}
	update, err := exactObject(applied["update"], "tables")
	if err != nil {
		return nil, ErrRecoveryPairs
	}
	tables, ok := update["tables"].([]any)
	if !ok || len(tables) < 1 || len(tables) > 3 {
		return nil, ErrRecoveryPairs
	}
	results := [][]any{{}, {}, {}}
	seen := make(map[int]bool, 3)
	ids := make(map[int]bool, 3)
	for _, value := range tables {
		index, rows, id, tableErr := decodeConfirmedSubscriptionTable(value, limits, layout)
		if tableErr != nil || seen[index] || ids[id] {
			return nil, ErrRecoveryPairs
		}
		seen[index], ids[id] = true, true
		results[index] = rows
	}
	if !seen[2] {
		return nil, ErrRecoveryPairs
	}
	return decodeConfirmedRecoveryResults(results)
}

func decodeConfirmedSubscriptionTable(value any, limits SchemaLimits, layout string) (int, []any, int, error) {
	table, err := exactObject(value, "table_id", "table_name", "num_rows", "updates")
	if err != nil {
		return 0, nil, 0, ErrRecoveryPairs
	}
	id, err := integer(table["table_id"])
	count, countErr := integer(table["num_rows"])
	name, ok := table["table_name"].(string)
	index := confirmedSubscriptionTableIndex(name)
	if err != nil || countErr != nil || !ok || index < 0 || count > MaxRecoveryPairs {
		return 0, nil, 0, ErrRecoveryPairs
	}
	updates, ok := table["updates"].([]any)
	if !ok || len(updates) != 1 {
		return 0, nil, 0, ErrRecoveryPairs
	}
	update, err := exactObject(updates[0], "deletes", "inserts")
	if err != nil {
		return 0, nil, 0, ErrRecoveryPairs
	}
	deletes, ok := update["deletes"].([]any)
	if !ok || len(deletes) != 0 {
		return 0, nil, 0, ErrRecoveryPairs
	}
	inserts, ok := update["inserts"].([]any)
	if !ok || len(inserts) != count {
		return 0, nil, 0, ErrRecoveryPairs
	}
	rows := make([]any, len(inserts))
	for i, value := range inserts {
		encoded, ok := value.(string)
		if !ok || len(encoded) > MaxRecoveryPairsBytes {
			return 0, nil, 0, ErrRecoveryPairs
		}
		row, decodeErr := decodeDocument([]byte(encoded), limits)
		rows[i], err = confirmedSubscriptionRow(row, index == 2, layout)
		if decodeErr != nil || err != nil {
			return 0, nil, 0, ErrRecoveryPairs
		}
	}
	return index, rows, id, nil
}

func confirmedSubscriptionTableIndex(name string) int {
	switch name {
	case "recovery_left":
		return 0
	case "recovery_right":
		return 1
	case "schema_marker":
		return 2
	default:
		return -1
	}
}

func confirmedSubscriptionRow(value any, marker bool, layout string) (any, error) {
	if marker {
		fields, err := exactObject(value, "id", "revision")
		if err != nil {
			return nil, ErrRecoveryPairs
		}
		return []any{fields["id"], fields["revision"]}, nil
	}
	names := []string{"sequence", "marker", "commit_time", "scheduled"}
	if layout == "rating-v1" {
		names = append(names, "rating")
	} else if layout != "base-v1" {
		return nil, ErrRecoveryPairs
	}
	fields, err := exactObject(value, names...)
	if err != nil {
		return nil, ErrRecoveryPairs
	}
	if layout == "rating-v1" {
		rating, err := integer(fields["rating"])
		if err != nil || rating != 7 {
			return nil, ErrRecoveryPairs
		}
	}
	stamp, err := exactObject(fields["commit_time"], "__timestamp_micros_since_unix_epoch__")
	if err != nil {
		return nil, ErrRecoveryPairs
	}
	return []any{fields["sequence"], fields["marker"], []any{stamp["__timestamp_micros_since_unix_epoch__"]}, fields["scheduled"]}, nil
}
