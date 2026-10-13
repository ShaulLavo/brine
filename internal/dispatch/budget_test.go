package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/datapublish"
)

type delayedRequestReader struct {
	io.Reader
	delay time.Duration
}

func (r *delayedRequestReader) Read(data []byte) (int, error) {
	time.Sleep(r.delay)
	r.delay = 0
	return r.Reader.Read(data)
}

func TestStandardHostBudgetIncludesRequestRead(t *testing.T) {
	request := nativeBackupRequest(t, "ping", "{}")
	reader := &delayedRequestReader{Reader: bytes.NewReader(request), delay: 200 * time.Millisecond}
	server := NewServer("fixture", nil)
	var deadline time.Time
	server.Factory = func(ctx context.Context, _ string) (*Server, error) {
		deadline, _ = ctx.Deadline()
		return nil, nil
	}
	started := time.Now()
	response, err := server.Handle(t.Context(), reader)
	if err != nil || !response.OK || deadline.Before(started.Add(RequestTimeout)) || deadline.After(started.Add(RequestTimeout+100*time.Millisecond)) {
		t.Fatal("standard host deadline restarted after request read", err)
	}
}

func TestOperationBudgetsKeepStandardCallsShort(t *testing.T) {
	for _, operation := range []string{"ping", "inventory", "job_status", "app_status", "module_publish_inspect", "module_publish_apply", "native_live_restore_plan", "foreign"} {
		if OperationTimeout(operation) != 15*time.Second || CallTimeout(operation) != 15*time.Second {
			t.Fatal("unrelated operation gained a review budget", operation)
		}
	}
	for _, operation := range []string{"module_publish_plan", "native_backup_inspect", "native_backup_review"} {
		if OperationTimeout(operation) != ReviewWorkTimeout || CallTimeout(operation) != RequestTimeout+ReviewWorkTimeout+ReviewCleanupTimeout+SSHConnectTimeout {
			t.Fatal("review budget does not cover read, work and teardown", operation)
		}
	}
}

type reviewBudgetRecorder struct {
	moduleOperationsRecorder
	deadline        time.Time
	started         chan struct{}
	cleaned         bool
	deadlineCleanup bool
}

func (r *reviewBudgetRecorder) Plan(ctx context.Context, args ModulePlanArgs) (datapublish.ReviewView, error) {
	r.deadline, _ = ctx.Deadline()
	if r.started == nil {
		return r.moduleOperationsRecorder.Plan(ctx, args)
	}
	close(r.started)
	<-ctx.Done()
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), ReviewCleanupTimeout)
	defer cancel()
	r.cleaned = cleanup.Err() == nil
	deadline, exists := cleanup.Deadline()
	r.deadlineCleanup = exists && time.Until(deadline) <= ReviewCleanupTimeout
	return datapublish.ReviewView{}, ctx.Err()
}

func budgetReviewServer(r *reviewBudgetRecorder) *Server {
	s := NewServer("fixture", nil).WithJobs(nil, func(context.Context, Class) error { return nil })
	s.ModulePublications = r
	return s
}

func TestReviewHostBudgetAndParentCancellation(t *testing.T) {
	raw, _ := json.Marshal(modulePlanInput())
	request := nativeBackupRequest(t, "module_publish_plan", string(raw))
	r := &reviewBudgetRecorder{}
	started := time.Now()
	response, err := budgetReviewServer(r).Handle(context.Background(), bytes.NewReader(request))
	if err != nil || !response.OK || r.deadline.Before(started.Add(ReviewWorkTimeout-time.Second)) || r.deadline.After(time.Now().Add(ReviewWorkTimeout)) {
		t.Fatal("host review work inherited the short request budget", err)
	}
	parent, cancel := context.WithCancel(t.Context())
	r = &reviewBudgetRecorder{started: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := budgetReviewServer(r).Handle(parent, bytes.NewReader(request)); done <- err }()
	<-r.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !r.cleaned || !r.deadlineCleanup {
			t.Fatal("review cancellation or bounded teardown was lost", err)
		}
	case <-time.After(time.Second):
		t.Fatal("parent cancellation failed to stop review work")
	}
}

func TestReviewHostPreservesEarlierParentDeadline(t *testing.T) {
	raw, _ := json.Marshal(modulePlanInput())
	request := nativeBackupRequest(t, "module_publish_plan", string(raw))
	deadline := time.Now().Add(time.Second)
	parent, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	r := &reviewBudgetRecorder{}
	response, err := budgetReviewServer(r).Handle(parent, bytes.NewReader(request))
	if err != nil || !response.OK || !r.deadline.Equal(deadline) {
		t.Fatal("host review extended its parent's deadline", err)
	}
}

func TestRequestReadCancellationRemainsBoundedBeforeOperation(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	done := make(chan error, 1)
	go func() { _, err := readRequest(parent, reader); done <- err }()
	if _, err := writer.Write([]byte("{")); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("request read lost parent cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request read remained open")
	}
}
