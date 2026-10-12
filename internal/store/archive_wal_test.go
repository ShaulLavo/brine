//go:build linux

package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestArchiveCandidateReadsLiveWALWhileAnotherHandleWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	writer := openTest(t)
	plan := storedArchivePlan(t, writer)
	for _, readOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("read_only_%t", readOnly), func(t *testing.T) {
			var reader *Store
			var err error
			if readOnly {
				reader, err = OpenReadOnly(ctx, writer.dir)
			} else {
				reader, err = Open(writer.dir)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := reader.Close(); err != nil {
					t.Error(err)
				}
			}()
			stop := make(chan struct{})
			done := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				for n := 0; n < 150; n++ {
					select {
					case <-stop:
						done <- nil
						return
					default:
					}
					if err := writer.RecordWriterAttempt(ctx, plan.Candidate.Incarnation, fmt.Sprintf("wal-%t-%d", readOnly, n)); err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			<-started
			defer func() {
				close(stop)
				if err := <-done; err != nil {
					t.Error(err)
				}
			}()
			for n := 0; n < 150; n++ {
				candidate, err := reader.ArchiveCandidate(ctx, plan.Candidate.App)
				if err != nil {
					t.Fatalf("live WAL candidate read %d: %v", n, err)
				}
				if candidate.Incarnation != plan.Candidate.Incarnation || len(candidate.Databases) != 1 || sqliteArchiveDatabase(t, candidate.Databases[0]).Binding != sqliteArchiveDatabase(t, plan.Candidate.Databases[0]).Binding {
					t.Fatal("inconsistent archive snapshot", candidate)
				}
			}
		})
	}
}
