package replication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

var ErrBarrier = errors.New("replication: remote upload barrier unknown")

// BarrierReceipt is upload evidence for one exact nonzero LTX point, not an
// isolated restore verification or a claim about the application's last commit.
type BarrierReceipt struct {
	DatabaseID, BindingID, EpochID string
	TXID, ReplicaTXID              uint64
	ObservedAt                     time.Time
}

func SyncBarrier(ctx context.Context, e localexec.Executor, b Binding, timeout time.Duration) (BarrierReceipt, error) {
	if e == nil || b.Validate() != nil || timeout < time.Second || timeout > 10*time.Minute {
		return BarrierReceipt{}, ErrBarrier
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	seconds := strconv.FormatInt(int64(timeout/time.Second), 10)
	r, err := e.Execute(ctx, localexec.Command{Path: Executable, Args: []string{"sync", "-wait", "-json", "-timeout", seconds, "-socket", b.SocketPath, b.DBPath}, Timeout: timeout, Mutation: true, OutputLimit: 16 * 1024})
	if err != nil || ctx.Err() != nil || r.Truncated || r.ExitCode != 0 || len(r.Stdout) > 16*1024 {
		return BarrierReceipt{}, ErrBarrier
	}
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewBufferString(r.Stdout))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return BarrierReceipt{}, ErrBarrier
	}
	fields = map[string]json.RawMessage{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return BarrierReceipt{}, ErrBarrier
		}
		name, ok := key.(string)
		if !ok {
			return BarrierReceipt{}, ErrBarrier
		}
		if _, exists := fields[name]; exists {
			return BarrierReceipt{}, ErrBarrier
		}
		var value json.RawMessage
		if dec.Decode(&value) != nil {
			return BarrierReceipt{}, ErrBarrier
		}
		fields[name] = value
	}
	if token, err = dec.Token(); err != nil || token != json.Delim('}') {
		return BarrierReceipt{}, ErrBarrier
	}
	if _, err = dec.Token(); err != io.EOF || len(fields) != 4 {
		return BarrierReceipt{}, ErrBarrier
	}
	var db string
	var txid, replica uint64
	var duration int64
	if json.Unmarshal(fields["db_path"], &db) != nil || json.Unmarshal(fields["txid"], &txid) != nil || json.Unmarshal(fields["replica_txid"], &replica) != nil || json.Unmarshal(fields["duration_ms"], &duration) != nil || db != b.DBPath || txid == 0 || replica < txid || duration < 0 {
		return BarrierReceipt{}, ErrBarrier
	}
	return BarrierReceipt{DatabaseID: b.DatabaseID, BindingID: b.BindingID, EpochID: b.EpochID, TXID: txid, ReplicaTXID: replica, ObservedAt: time.Now().UTC()}, nil
}
