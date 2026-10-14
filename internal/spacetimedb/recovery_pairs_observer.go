package spacetimedb

import (
	"bytes"
	"context"
	"encoding/json"
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
	view, viewErr := canonicalView(registered)
	if viewErr != nil {
		return ConfirmedPairsObservation{}, ErrRecoveryPairs
	}
	layout, layoutErr := recoverySchemaLayout(namedRows(view["Tables"]))
	if layoutErr != nil {
		return ConfirmedPairsObservation{}, ErrRecoveryPairs
	}
	rows, err := readConfirmedSubscription(ctx, endpoint, credential, limits, layout)
	if err != nil {
		return ConfirmedPairsObservation{}, ErrRecoveryPairs
	}
	after, err := ObserveCurrent(ctx, endpoint, credential, expected, limits)
	if err != nil || !confirmedModuleMatches(after, program, registered) || before.Database() != after.Database() || before.Publisher() != after.Publisher() || !slices.Equal(before.Names(), after.Names()) {
		return ConfirmedPairsObservation{}, ErrRecoveryPairs
	}
	record, err := newConfirmedPairsRecord(confirmedPairsRecord{
		Format: ConfirmedPairsFormat, Contract: RecoveryPairsContract, Basis: "confirmed_subscription_lower_bound",
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
