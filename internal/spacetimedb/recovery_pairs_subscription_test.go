package spacetimedb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func subscriptionSnapshotFixture(t *testing.T) map[string]any {
	t.Helper()
	results := recoverySQLResults(t, nativeRecoverySQL(t))
	names := []string{"recovery_left", "recovery_right", "schema_marker"}
	tables := make([]any, 3)
	for i, name := range names {
		inserts := make([]any, len(results[i]))
		for j, row := range results[i] {
			var value any
			columns := row.([]any)
			if i == 2 {
				value = map[string]any{"id": columns[0], "revision": columns[1]}
			} else {
				value = map[string]any{"sequence": columns[0], "marker": columns[1], "commit_time": map[string]any{"__timestamp_micros_since_unix_epoch__": columns[2].([]any)[0]}, "scheduled": columns[3]}
			}
			raw, _ := json.Marshal(value)
			inserts[j] = string(raw)
		}
		tables[i] = map[string]any{"table_id": i + 1, "table_name": name, "num_rows": len(inserts), "updates": []any{map[string]any{"deletes": []any{}, "inserts": inserts}}}
	}
	return map[string]any{"SubscribeMultiApplied": map[string]any{"request_id": 1, "query_id": map[string]any{"id": 1}, "total_host_execution_duration_micros": 1, "update": map[string]any{"tables": tables}}}
}

func encodeSubscriptionFixture(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func subscriptionTables(value map[string]any) []any {
	return value["SubscribeMultiApplied"].(map[string]any)["update"].(map[string]any)["tables"].([]any)
}

func TestConfirmedSubscriptionDecodesCompleteCoherentSnapshot(t *testing.T) {
	rows, err := decodeConfirmedSubscription(encodeSubscriptionFixture(t, subscriptionSnapshotFixture(t)), DefaultSchemaLimits(), "base-v1")
	if err != nil || len(rows) != 3 || rows[0].Sequence != "1" {
		t.Fatal("coherent independent rows refused", err)
	}
}

func TestConfirmedSubscriptionRefusesCrossedAndMalformedSnapshots(t *testing.T) {
	attacks := map[string]func(map[string]any){
		"request id": func(v map[string]any) { v["SubscribeMultiApplied"].(map[string]any)["request_id"] = 2 },
		"query id": func(v map[string]any) {
			v["SubscribeMultiApplied"].(map[string]any)["query_id"] = map[string]any{"id": 2}
		},
		"missing table": func(v map[string]any) {
			v["SubscribeMultiApplied"].(map[string]any)["update"].(map[string]any)["tables"] = subscriptionTables(v)[:2]
		},
		"duplicate table":    func(v map[string]any) { subscriptionTables(v)[1] = subscriptionTables(v)[0] },
		"foreign table":      func(v map[string]any) { subscriptionTables(v)[0].(map[string]any)["table_name"] = "foreign" },
		"duplicate table id": func(v map[string]any) { subscriptionTables(v)[1].(map[string]any)["table_id"] = 1 },
		"orphan right row": func(v map[string]any) {
			table := subscriptionTables(v)[1].(map[string]any)
			table["num_rows"] = 2
			table["updates"].([]any)[0].(map[string]any)["inserts"] = table["updates"].([]any)[0].(map[string]any)["inserts"].([]any)[:2]
		},
		"wrong row count": func(v map[string]any) { subscriptionTables(v)[0].(map[string]any)["num_rows"] = 2 },
		"deletion": func(v map[string]any) {
			subscriptionTables(v)[0].(map[string]any)["updates"].([]any)[0].(map[string]any)["deletes"] = []any{"[1]"}
		},
		"changed marker": func(v map[string]any) {
			subscriptionTables(v)[2].(map[string]any)["updates"].([]any)[0].(map[string]any)["inserts"] = []any{`[1,"foreign"]`}
		},
		"malformed row": func(v map[string]any) {
			subscriptionTables(v)[0].(map[string]any)["updates"].([]any)[0].(map[string]any)["inserts"].([]any)[0] = `[1]`
		},
		"extra field": func(v map[string]any) { subscriptionTables(v)[0].(map[string]any)["future"] = true },
	}
	for name, attack := range attacks {
		t.Run(name, func(t *testing.T) {
			value := subscriptionSnapshotFixture(t)
			attack(value)
			if _, err := decodeConfirmedSubscription(encodeSubscriptionFixture(t, value), DefaultSchemaLimits(), "base-v1"); !errors.Is(err, ErrRecoveryPairs) {
				t.Fatal("crossed snapshot admitted", err)
			}
		})
	}
	for _, raw := range [][]byte{[]byte(`{"SubscribeMultiApplied":{},"SubscribeMultiApplied":{}}`), []byte(strings.Repeat("x", MaxRecoveryPairsBytes+1)), []byte(`{"SubscriptionError":{}}`)} {
		if _, err := decodeConfirmedSubscription(raw, DefaultSchemaLimits(), "base-v1"); !errors.Is(err, ErrRecoveryPairs) {
			t.Fatal("invalid frame admitted", err)
		}
	}
}

func TestConfirmedSubscriptionUsesOneDurableSnapshotWhileWriterAdvances(t *testing.T) {
	snapshot := encodeSubscriptionFixture(t, subscriptionSnapshotFixture(t))
	requested := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("confirmed") != "true" || r.URL.Query().Get("compression") != "None" || r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{confirmedSubscriptionProtocol}})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"IdentityToken":{"identity":{"__identity__":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"token":"test-token","connection_id":{"__connection_id__":1}}}`))
		kind, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var request map[string]json.RawMessage
		requested <- kind == websocket.MessageText && json.Unmarshal(raw, &request) == nil && request["SubscribeMulti"] != nil
		_ = conn.Write(ctx, websocket.MessageText, snapshot)
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	port, _ := strconv.ParseUint(parsed.Port(), 10, 16)
	endpoint, _ := NewPrivateEndpoint("127.0.0.1", uint16(port), strings.Repeat("a", 64))
	credential := PublisherCredential{bearer: "test-token"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rows, err := readConfirmedSubscription(ctx, endpoint, credential, DefaultObserverLimits(), "base-v1")
	if err != nil || len(rows) != 3 || !<-requested {
		t.Fatal("coherent subscription failed", err)
	}
}

func TestConfirmedSubscriptionKeepsEmptyTablesAndOrphansDistinct(t *testing.T) {
	value := subscriptionSnapshotFixture(t)
	marker := subscriptionTables(value)[2]
	applied := value["SubscribeMultiApplied"].(map[string]any)
	applied["update"].(map[string]any)["tables"] = []any{marker}
	rows, err := decodeConfirmedSubscription(encodeSubscriptionFixture(t, value), DefaultSchemaLimits(), "base-v1")
	if err != nil || len(rows) != 0 {
		t.Fatal("pinned empty-table omission refused", err)
	}
	value = subscriptionSnapshotFixture(t)
	tables := subscriptionTables(value)
	value["SubscribeMultiApplied"].(map[string]any)["update"].(map[string]any)["tables"] = []any{tables[0], tables[2]}
	if _, err := decodeConfirmedSubscription(encodeSubscriptionFixture(t, value), DefaultSchemaLimits(), "base-v1"); !errors.Is(err, ErrRecoveryPairs) {
		t.Fatal("orphan populated table admitted", err)
	}
}

func TestConfirmedSubscriptionQualifiedDefaultColumn(t *testing.T) {
	value := subscriptionSnapshotFixture(t)
	for _, tableValue := range subscriptionTables(value)[:2] {
		table := tableValue.(map[string]any)
		inserts := table["updates"].([]any)[0].(map[string]any)["inserts"].([]any)
		for i, encoded := range inserts {
			var row map[string]any
			if err := json.Unmarshal([]byte(encoded.(string)), &row); err != nil {
				t.Fatal(err)
			}
			row["rating"] = 7
			raw, _ := json.Marshal(row)
			inserts[i] = string(raw)
		}
	}
	raw := encodeSubscriptionFixture(t, value)
	if _, err := decodeConfirmedSubscription(raw, DefaultSchemaLimits(), "rating-v1"); err != nil {
		t.Fatal("qualified default column refused", err)
	}
	if _, err := decodeConfirmedSubscription(raw, DefaultSchemaLimits(), "base-v1"); !errors.Is(err, ErrRecoveryPairs) {
		t.Fatal("unqualified extra column admitted", err)
	}
	table := subscriptionTables(value)[0].(map[string]any)
	inserts := table["updates"].([]any)[0].(map[string]any)["inserts"].([]any)
	var row map[string]any
	if err := json.Unmarshal([]byte(inserts[0].(string)), &row); err != nil {
		t.Fatal(err)
	}
	row["rating"] = 8
	changed, _ := json.Marshal(row)
	inserts[0] = string(changed)
	if _, err := decodeConfirmedSubscription(encodeSubscriptionFixture(t, value), DefaultSchemaLimits(), "rating-v1"); !errors.Is(err, ErrRecoveryPairs) {
		t.Fatal("changed default value admitted", err)
	}
}

func TestConfirmedSubscriptionDeadlineAndDenialRefuseWithoutRetry(t *testing.T) {
	for _, name := range []string{"denied", "missing snapshot", "binary snapshot"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if name == "denied" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{confirmedSubscriptionProtocol}})
				if err != nil {
					return
				}
				defer func() { _ = conn.CloseNow() }()
				_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"IdentityToken":{"identity":{"__identity__":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"token":"test-token","connection_id":{"__connection_id__":1}}}`))
				_, _, _ = conn.Read(r.Context())
				if name == "binary snapshot" {
					_ = conn.Write(r.Context(), websocket.MessageBinary, []byte("invalid"))
					return
				}
				_, _, _ = conn.Read(r.Context())
			}))
			defer server.Close()
			parsed, _ := url.Parse(server.URL)
			port, _ := strconv.ParseUint(parsed.Port(), 10, 16)
			endpoint, _ := NewPrivateEndpoint("127.0.0.1", uint16(port), strings.Repeat("a", 64))
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err := readConfirmedSubscription(ctx, endpoint, PublisherCredential{bearer: "test-token"}, DefaultObserverLimits(), "base-v1")
			if !errors.Is(err, ErrRecoveryPairs) || calls != 1 {
				t.Fatal("failed subscription retried or admitted", err, calls)
			}
		})
	}
}

func TestConfirmedSubscriptionIdentityFrameIsClosedAndTyped(t *testing.T) {
	valid := []byte(`{"IdentityToken":{"identity":{"__identity__":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"token":"test-token","connection_id":{"__connection_id__":340282366920938463463374607431768211455}}}`)
	if err := checkSubscriptionIdentity(valid, DefaultSchemaLimits()); err != nil {
		t.Fatal("pinned identity frame refused", err)
	}
	for _, changed := range [][]byte{
		[]byte(strings.Replace(string(valid), "340282366920938463463374607431768211455", "340282366920938463463374607431768211456", 1)),
		[]byte(strings.Replace(string(valid), "\"token\":\"test-token\"", "\"token\":null", 1)),
		[]byte(strings.Replace(string(valid), "\"__identity__\"", "\"foreign\"", 1)),
		[]byte(strings.Replace(string(valid), "\"IdentityToken\"", "\"TransactionUpdate\"", 1)),
	} {
		if err := checkSubscriptionIdentity(changed, DefaultSchemaLimits()); !errors.Is(err, ErrRecoveryPairs) {
			t.Fatal("malformed identity admitted", err)
		}
	}
}
