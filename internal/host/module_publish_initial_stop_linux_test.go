//go:build linux

package host

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/localexec"
)

type initialStopExecutor struct{ calls int }

func (e *initialStopExecutor) Execute(_ context.Context, _ localexec.Command) (localexec.Result, error) {
	e.calls++
	return localexec.Result{}, context.DeadlineExceeded
}

func TestInitialPublisherStopLatchTracksCommandIssuance(t *testing.T) {
	ctx := context.Background()
	lease := &publicationStageLease{initialStage: &nativeInitialStageWork{}}
	if stopPublicationStageContainer(ctx, lease, localexec.Session{}, "container") == nil {
		t.Fatal("missing durable initial journal admitted a stop")
	}
	if lease.stopAttempted {
		t.Fatal("failed journal marked unissued stop attempted")
	}
	executor := &initialStopExecutor{}
	uid, err := strconv.ParseUint(strconv.Itoa(os.Geteuid()), 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	session, err := localexec.NewSession(executor, uint32(uid), t.TempDir(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	lease = &publicationStageLease{}
	if stopPublicationStageContainer(ctx, lease, session, "container") == nil || !lease.stopAttempted {
		t.Fatal("uncertain issued stop lost its latch")
	}
	if stopPublicationStageContainer(ctx, lease, session, "container") == nil || executor.calls != 1 {
		t.Fatal("unknown stop was repeated")
	}
}
