//go:build linux

package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/ops"
	"modernc.org/sqlite"
)

func TestStoreReplacementConnectionKeepsPragmas(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	for attempt := 0; attempt < 2; attempt++ {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for pragma, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "busy_timeout": "5000"} {
			var got string
			if err := conn.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
				conn.Close()
				t.Fatalf("connection %d %s = %s, want %s, error %v", attempt, pragma, got, want, err)
			}
		}
		if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
			t.Fatal(err)
		}
		conn.Close()
	}
}

func TestStoreWriteTransactionWaitsBeforeReading(t *testing.T) {
	a := openTest(t)
	b, err := Open(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for attempt := 0; attempt < 2; attempt++ {
		tx, err := a.db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		result := make(chan error, 1)
		started := make(chan struct{})
		go func() {
			close(started)
			contended, err := b.db.BeginTx(ctx, nil)
			if err == nil {
				err = contended.Rollback()
			}
			result <- err
		}()
		<-started
		select {
		case err := <-result:
			tx.Rollback()
			cancel()
			t.Fatalf("connection %d entered or failed before the writer released its lock: %v", attempt, err)
		case <-time.After(200 * time.Millisecond):
		}
		if err := tx.Rollback(); err != nil {
			cancel()
			t.Fatal(err)
		}
		if err := <-result; err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		conn, err := b.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		conn.Raw(func(any) error { return driver.ErrBadConn })
		conn.Close()
	}
}

func TestStoreContendedJournalWritesWait(t *testing.T) {
	for _, transition := range []bool{false, true} {
		name := "append"
		if transition {
			name = "transition"
		}
		t.Run(name, func(t *testing.T) {
			a := openTest(t)
			id := operation(t, a)
			b, err := Open(a.dir)
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			child := startHelper(t, a.dir, id, "state-uncommitted")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			result := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				if transition {
					result <- b.TransitionOperation(ctx, id, ops.Queued, ops.Preflight)
				} else {
					_, err := b.AppendEvent(ctx, id, Event{Kind: "launch", Payload: []byte(`{"outcome":"intent"}`)})
					result <- err
				}
			}()
			<-started
			select {
			case err := <-result:
				t.Fatalf("journal write finished before the writer released its lock: %v", err)
			case <-time.After(200 * time.Millisecond):
			}
			killHelper(t, child)
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			events, err := a.EventsAfter(context.Background(), id, 0, 10)
			if err != nil || len(events) != 1 || events[0].Sequence != 1 {
				t.Fatalf("journal after contention = %+v, error %v", events, err)
			}
			if transition && (events[0].Kind != "state" || events[0].State != ops.Preflight) {
				t.Fatalf("transition journal = %+v", events)
			}
		})
	}
}

func TestStoreBusyBudgetRefusesWithoutJournalEffect(t *testing.T) {
	a := openTest(t)
	id := operation(t, a)
	b, err := Open(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tx, err := a.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	started := time.Now()
	_, err = b.db.BeginTx(context.Background(), nil)
	var busy *sqlite.Error
	if !errors.As(err, &busy) || busy.Code() != 5 {
		t.Fatalf("held writer returned %v, want SQLITE_BUSY", err)
	}
	if elapsed := time.Since(started); elapsed < 5*time.Second {
		t.Fatalf("busy budget expired after %s, want at least 5s", elapsed)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	events, err := a.EventsAfter(context.Background(), id, 0, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("refused append changed journal = %+v, error %v", events, err)
	}
	seq, err := b.AppendEvent(context.Background(), id, Event{Kind: "launch", Payload: []byte(`{"outcome":"intent"}`)})
	if err != nil || seq != 1 {
		t.Fatalf("append after release = %d, error %v", seq, err)
	}
}

func TestStoreWriteAdmissionRetriesRefusedBegin(t *testing.T) {
	a := openTest(t)
	id := operation(t, a)
	b, err := Open(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tx, err := a.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := b.AppendEvent(ctx, id, Event{Kind: "launch", Payload: []byte(`{"outcome":"intent"}`)})
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("admission gave up before lock release: %v", err)
	case <-time.After(5500 * time.Millisecond):
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	events, err := a.EventsAfter(ctx, id, 0, 10)
	if err != nil || len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("journal = %+v, error %v", events, err)
	}
}

func TestStoreWriteAdmissionRespectsCancellation(t *testing.T) {
	a := openTest(t)
	id := operation(t, a)
	b, err := Open(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tx, err := a.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = b.AppendEvent(ctx, id, Event{Kind: "launch", Payload: []byte(`{"outcome":"intent"}`)})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline returned %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	events, err := a.EventsAfter(context.Background(), id, 0, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("cancelled admission journal = %+v, error %v", events, err)
	}
}

func TestConcurrentRuntimeStoresShareJournal(t *testing.T) {
	dispatcher := openTest(t)
	planID, _ := saved(t, dispatcher)
	stores := []*Store{dispatcher}
	for i := 0; i < 4; i++ {
		s, err := Open(dispatcher.dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		stores = append(stores, s)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 8; j++ {
				op, _, err := s.CreateOperation(ctx, ops.Intent{Kind: ops.Deploy, PlanID: planID}, "runtime", "shared")
				if err != nil {
					t.Errorf("runtime store %d create %d: %v", i, j, err)
					return
				}
				if _, err := s.AppendEvent(ctx, op.ID, Event{Kind: "launch", Payload: []byte(`{"outcome":"intent"}`)}); err != nil {
					t.Errorf("runtime store %d append %d: %v", i, j, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	op, _, err := dispatcher.CreateOperation(context.Background(), ops.Intent{Kind: ops.Deploy, PlanID: planID}, "runtime", "shared")
	if err != nil {
		t.Fatal(err)
	}
	events, err := dispatcher.EventsAfter(context.Background(), op.ID, 0, 100)
	if err != nil || len(events) != 40 {
		t.Fatalf("runtime journal has %d events, error %v", len(events), err)
	}
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatal("runtime journal sequence gap")
		}
	}
}
