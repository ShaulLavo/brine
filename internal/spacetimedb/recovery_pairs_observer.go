package spacetimedb

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"
)

func ObserveConfirmedPairs(ctx context.Context, endpoint PrivateEndpoint, credential PublisherCredential, expected ExpectedDatabase, artifact []byte, registered CanonicalSchema, limits ObserverLimits) (ConfirmedPairsObservation, error) {
	if ctx.Err() != nil || len(artifact) > MaxWasmBytes || limits.Timeout <= 0 || limits.Timeout > 30*time.Second || !limits.Schema.valid() {
		return ConfirmedPairsObservation{}, ErrRecoveryPairs
	}
	wasm, program, err := ModuleIdentities(bytes.Clone(artifact))
	if err != nil || registered.Version() != CanonicalizerVersion || !SupportsConfirmedRecoveryPairs(string(wasm), registered.SHA256()) {
		return ConfirmedPairsObservation{}, ErrRecoveryPairs
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	before, err := ObserveCurrent(ctx, endpoint, credential, expected, limits)
	if err != nil || !confirmedModuleMatches(before, program, registered) {
		return ConfirmedPairsObservation{}, ErrRecoveryPairs
	}
	rows, err := readConfirmedPairs(ctx, endpoint, credential, limits)
	if err != nil {
		return ConfirmedPairsObservation{}, ErrRecoveryPairs
	}
	after, err := ObserveCurrent(ctx, endpoint, credential, expected, limits)
	if err != nil || !confirmedModuleMatches(after, program, registered) || before.Database() != after.Database() || before.Publisher() != after.Publisher() || !slices.Equal(before.Names(), after.Names()) {
		return ConfirmedPairsObservation{}, ErrRecoveryPairs
	}
	record, err := newConfirmedPairsRecord(confirmedPairsRecord{
		Format: ConfirmedPairsFormat, Contract: RecoveryPairsContract, Basis: "confirmed_sql_lower_bound",
		Database: string(before.Database()), Publisher: string(before.Publisher()), Names: before.Names(),
		Program: string(program), Wasm: string(wasm), Canonicalizer: registered.Version(), Schema: registered.SHA256(), Marker: RecoverySchemaMarker, Rows: rows,
	})
	if err != nil {
		return ConfirmedPairsObservation{}, err
	}
	return ConfirmedPairsObservation{record: record}, nil
}

func confirmedModuleMatches(observed Observation, program RuntimeModuleHash, registered CanonicalSchema) bool {
	return observed.HostType() == "Wasm" && observed.CurrentModule() == program && observed.Schema().Version() == registered.Version() && observed.Schema().SHA256() == registered.SHA256()
}

func readConfirmedPairs(ctx context.Context, endpoint PrivateEndpoint, credential PublisherCredential, limits ObserverLimits) ([]recoveryPair, error) {
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10, ResponseHeaderTimeout: limits.Timeout}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRecoveryPairs }}
	queries := []string{
		"SELECT sequence, marker, commit_time, scheduled FROM recovery_left",
		"SELECT sequence, marker, commit_time, scheduled FROM recovery_right",
		"SELECT id, revision FROM schema_marker",
	}
	results := make([][]any, 0, len(queries))
	remaining := MaxRecoveryPairsBytes
	for index, query := range queries {
		rows, err := readConfirmedSQL(ctx, &client, endpoint, credential, query, index == 2, limits.Schema, &remaining)
		if err != nil {
			return nil, err
		}
		results = append(results, rows)
	}
	return decodeConfirmedRecoveryResults(results)
}

func readConfirmedSQL(ctx context.Context, client *http.Client, endpoint PrivateEndpoint, credential PublisherCredential, query string, marker bool, limits SchemaLimits, remaining *int) ([]any, error) {
	address := endpoint.base + "/v1/database/" + url.PathEscape(string(endpoint.database)) + "/sql?confirmed=true"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewBufferString(query))
	if err != nil || *remaining <= 0 {
		return nil, ErrRecoveryPairs
	}
	request.Header.Set("Authorization", "Bearer "+credential.bearer)
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, ErrRecoveryPairs
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, ErrRecoveryPairs
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, int64(*remaining)+1))
	if err != nil || len(raw) > *remaining {
		return nil, ErrRecoveryPairs
	}
	*remaining -= len(raw)
	value, err := decodeDocument(raw, limits)
	if err != nil {
		return nil, ErrRecoveryPairs
	}
	return decodeConfirmedSQLStatement(value, marker)
}

func decodeConfirmedSQLStatement(value any, marker bool) ([]any, error) {
	statements, ok := value.([]any)
	if !ok || len(statements) != 1 {
		return nil, ErrRecoveryPairs
	}
	statement, err := exactObject(statements[0], "schema", "rows", "total_duration_micros", "stats")
	if err != nil || !confirmedSQLShape(statement["schema"], marker) || !confirmedSQLStats(statement["stats"]) {
		return nil, ErrRecoveryPairs
	}
	if _, err := integer(statement["total_duration_micros"]); err != nil {
		return nil, ErrRecoveryPairs
	}
	rows, ok := statement["rows"].([]any)
	if !ok || len(rows) > MaxRecoveryPairs {
		return nil, ErrRecoveryPairs
	}
	return rows, nil
}

func confirmedSQLStats(value any) bool {
	stats, err := exactObject(value, "rows_inserted", "rows_deleted", "rows_updated")
	if err != nil {
		return false
	}
	for _, value := range stats {
		count, err := integer(value)
		if err != nil || count != 0 {
			return false
		}
	}
	return true
}

func confirmedSQLShape(value any, marker bool) bool {
	const pairs = `{"elements":[{"name":{"some":"sequence"},"algebraic_type":{"U64":[]}},{"name":{"some":"marker"},"algebraic_type":{"String":[]}},{"name":{"some":"commit_time"},"algebraic_type":{"Product":{"elements":[{"name":{"some":"__timestamp_micros_since_unix_epoch__"},"algebraic_type":{"I64":[]}}]}}},{"name":{"some":"scheduled"},"algebraic_type":{"Bool":[]}}]}`
	const markers = `{"elements":[{"name":{"some":"id"},"algebraic_type":{"U8":[]}},{"name":{"some":"revision"},"algebraic_type":{"String":[]}}]}`
	shape := pairs
	if marker {
		shape = markers
	}
	expected, err := decodeDocument([]byte(shape), DefaultSchemaLimits())
	if err != nil {
		return false
	}
	left, leftErr := encodeDocument(expected, MaxRecoveryPairsBytes)
	right, rightErr := encodeDocument(value, MaxRecoveryPairsBytes)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func decodeConfirmedRecoveryResults(results [][]any) ([]recoveryPair, error) {
	if len(results) != 3 || !validRecoveryMarker(results[2]) {
		return nil, ErrRecoveryPairs
	}
	left, err := decodeConfirmedRecoveryRows(results[0])
	if err != nil {
		return nil, err
	}
	right, err := decodeConfirmedRecoveryRows(results[1])
	if err != nil || !slices.Equal(left, right) || validateRecoveryPairs(left) != nil {
		return nil, ErrRecoveryPairs
	}
	return left, nil
}

func validRecoveryMarker(rows []any) bool {
	if len(rows) != 1 {
		return false
	}
	row, ok := rows[0].([]any)
	if !ok || len(row) != 2 || row[1] != RecoverySchemaMarker {
		return false
	}
	id, err := integer(row[0])
	return err == nil && id == 1
}

func decodeConfirmedRecoveryRows(rows []any) ([]recoveryPair, error) {
	pairs := make([]recoveryPair, 0, len(rows))
	for _, value := range rows {
		row, err := decodeConfirmedRecoveryRow(value)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, row)
	}
	slices.SortFunc(pairs, func(left, right recoveryPair) int {
		leftSequence, _ := strconv.ParseUint(left.Sequence, 10, 64)
		rightSequence, _ := strconv.ParseUint(right.Sequence, 10, 64)
		if leftSequence < rightSequence {
			return -1
		}
		if leftSequence > rightSequence {
			return 1
		}
		return 0
	})
	return pairs, nil
}

func decodeConfirmedRecoveryRow(value any) (recoveryPair, error) {
	row, ok := value.([]any)
	if !ok || len(row) != 4 {
		return recoveryPair{}, ErrRecoveryPairs
	}
	sequence, err := integer(row[0])
	marker, markerOK := row[1].(string)
	timestamp, timestampOK := row[2].([]any)
	scheduled, scheduledOK := row[3].(bool)
	if err != nil || sequence == 0 || sequence > MaxRecoveryPairs || !markerOK || !timestampOK || len(timestamp) != 1 || !scheduledOK {
		return recoveryPair{}, ErrRecoveryPairs
	}
	micros, ok := timestamp[0].(json.Number)
	if !ok {
		return recoveryPair{}, ErrRecoveryPairs
	}
	return recoveryPair{Sequence: strconv.Itoa(sequence), Marker: marker, CommitTime: micros.String(), Scheduled: scheduled}, nil
}
