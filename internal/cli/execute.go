package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ShaulLavo/brine/internal/dispatch"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/replication"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/spf13/cobra"
)

// Execute owns presentation for one invocation, including parser failures.
// Machine output is buffered until execution finishes so failures cannot follow
// a partial success response. An unavailable stdout still returns a write error.
func Execute(deps Dependencies, args []string) error {
	return ExecuteWithRuntime(deps, args, RuntimeLifecycle{})
}

// ExecuteWithRuntime initializes local host services only when validated command
// handlers use them, and closes all services before committing a response.
func ExecuteWithRuntime(deps Dependencies, args []string, lifecycle RuntimeLifecycle) error {
	runtime := invocationRuntime{lifecycle: lifecycle}
	if lifecycle.Open != nil {
		deps.HostOperationRunner = lazyOperationRunner{&runtime}
		deps.HostReconciler = lazyReconciler{&runtime}
		deps.HostDataInitialization = lazyDataInitialization{&runtime}
	}
	if lifecycle.OpenPermits != nil {
		deps.HostPermits = lazyHostPermits{&runtime}
	}
	if lifecycle.OpenWriterAttempt != nil {
		deps.HostWriterAttempt = lazyWriterAttempt{&runtime}.WriterAttempt
	}
	// Forced-command requests cannot select presentation or options through argv.
	if HostServeRequested(args) {
		return executeHostServeFinalized(deps, func(err error) error { return runtime.finalize(deps, err) })
	}
	modes := requestedModes(args)
	machine := modes.enabled()
	stdout := deps.Stdout
	var output bytes.Buffer
	buffered := machine || HostRuntimeRequested(args) || HostStartupRequested(args)
	if buffered {
		deps.Stdout = &output
	}
	root := NewRootCommand(deps)
	root.SetArgs(append([]string{}, args...))

	name := "brine"
	var command *cobra.Command
	var err error
	if modes.json && modes.jsonl {
		command, _, _ = root.Find(args)
		err = result.New(result.InvalidUsage, nil)
	} else {
		command, err = root.ExecuteC()
	}
	if command != nil {
		name = command.CommandPath()
	}
	if err != nil {
		if _, _, findErr := root.Find(args); findErr != nil {
			err = result.New(result.InvalidUsage, err)
		} else if command != nil && command.Name() == cobra.ShellCompRequestCmd {
			// Cobra adds this command during execution; its only validator is MinimumNArgs.
			err = result.New(result.InvalidUsage, err)
		}
	}
	if err == nil && deps.Context.Err() != nil {
		err = deps.Context.Err()
	}
	// Local recovery emits an envelope even without a presentation flag.
	if buffered && !machine && json.Valid(output.Bytes()) {
		machine = true
	}
	err = runtime.finalize(deps, err)
	if err != nil {
		err = result.Classify(err)
	}
	if machine {
		var writeErr error
		if err != nil {
			writeErr = json.NewEncoder(stdout).Encode(result.Failure(name, err))
		} else if json.Valid(output.Bytes()) || (modes.jsonl && validEnvelopeStream(output.Bytes(), name)) {
			_, writeErr = stdout.Write(output.Bytes())
		} else {
			writeErr = json.NewEncoder(stdout).Encode(result.Success(name, map[string]string{"help": output.String()}))
		}
		if writeErr != nil {
			err = result.New(result.InternalError, writeErr)
		}
	}
	if buffered && !machine && err == nil {
		if _, writeErr := stdout.Write(output.Bytes()); writeErr != nil {
			err = result.New(result.InternalError, writeErr)
		}
	}
	if err != nil {
		fmt.Fprintln(deps.Stderr, "error:", err)
	}
	return err
}

func usageError(_ *cobra.Command, err error) error { return result.New(result.InvalidUsage, err) }

func validEnvelopeStream(data []byte, command string) bool {
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return false
	}
	for _, line := range bytes.Split(data[:len(data)-1], []byte("\n")) {
		var e result.Envelope
		if json.Unmarshal(line, &e) != nil || e.SchemaVersion != result.SchemaVersion || e.Command != command || !e.OK || e.Error != nil {
			return false
		}
	}
	return true
}

type RuntimeServices struct {
	DataInitialization dispatch.DataInitializationOperations
	Permits            replication.LaunchReader
	WriterAttempt      func(context.Context, string) error
	Runner             OperationRunner
	Reconciler         dispatch.ReconcileOperations
	Close              func() error
}
type RuntimeLifecycle struct {
	OpenPermits       func(context.Context) (RuntimeServices, error)
	OpenWriterAttempt func(context.Context) (RuntimeServices, error)
	Open              func(context.Context, bool) (RuntimeServices, error)
	Close             func() error
}

type invocationRuntime struct {
	lifecycle   RuntimeLifecycle
	services    RuntimeServices
	initialized bool
	err         error
}

func (r *invocationRuntime) initialize(ctx context.Context, preview bool) error {
	if !r.initialized {
		r.initialized = true
		r.services, r.err = r.lifecycle.Open(ctx, preview)
	}
	return r.err
}
func (r *invocationRuntime) finalize(deps Dependencies, primary error) error {
	var closeErrors []error
	if r.services.Close != nil {
		closeErrors = append(closeErrors, r.services.Close())
	}
	if r.lifecycle.Close != nil {
		closeErrors = append(closeErrors, r.lifecycle.Close())
	}
	if secondary := errors.Join(closeErrors...); secondary != nil {
		if primary == nil {
			return result.New(result.InternalError, secondary)
		}
		fmt.Fprintln(deps.Stderr, "runtime cleanup failed:", result.InternalError)
	}
	return primary
}

type lazyOperationRunner struct{ runtime *invocationRuntime }

func (r lazyOperationRunner) Run(ctx context.Context, id string) error {
	if err := r.runtime.initialize(ctx, false); err != nil {
		return err
	}
	if r.runtime.services.Runner == nil {
		return result.New(result.DependencyMissing, nil)
	}
	return r.runtime.services.Runner.Run(ctx, id)
}

type lazyReconciler struct{ runtime *invocationRuntime }

func (r lazyReconciler) Reconcile(ctx context.Context) (reconcile.Report, error) {
	if err := r.runtime.initialize(ctx, false); err != nil {
		return reconcile.Report{}, err
	}
	if r.runtime.services.Reconciler == nil {
		return reconcile.Report{}, result.New(result.DependencyMissing, nil)
	}
	return r.runtime.services.Reconciler.Reconcile(ctx)
}
func (r lazyReconciler) DryRun(ctx context.Context) (reconcile.Report, error) {
	if err := r.runtime.initialize(ctx, true); err != nil {
		return reconcile.Report{}, err
	}
	if r.runtime.services.Reconciler == nil {
		return reconcile.Report{}, result.New(result.DependencyMissing, nil)
	}
	return r.runtime.services.Reconciler.DryRun(ctx)
}

func (r *invocationRuntime) initializeStartup(ctx context.Context, open func(context.Context) (RuntimeServices, error)) error {
	if !r.initialized {
		r.initialized = true
		if open == nil {
			r.err = replication.ErrPermit
		} else {
			r.services, r.err = open(ctx)
		}
	}
	return r.err
}

type lazyWriterAttempt struct{ runtime *invocationRuntime }

func (a lazyWriterAttempt) WriterAttempt(ctx context.Context, id string) error {
	if err := a.runtime.initializeStartup(ctx, a.runtime.lifecycle.OpenWriterAttempt); err != nil {
		return err
	}
	if a.runtime.services.WriterAttempt == nil {
		return replication.ErrPermit
	}
	return a.runtime.services.WriterAttempt(ctx, id)
}

type lazyHostPermits struct{ runtime *invocationRuntime }

func (p lazyHostPermits) reader(ctx context.Context) (replication.LaunchReader, error) {
	if err := p.runtime.initializeStartup(ctx, p.runtime.lifecycle.OpenPermits); err != nil {
		return nil, err
	}
	if p.runtime.services.Permits == nil {
		return nil, replication.ErrPermit
	}
	return p.runtime.services.Permits, nil
}
func (p lazyHostPermits) ReadReplicaPermit(ctx context.Context, id string) (replication.PermitState, error) {
	r, err := p.reader(ctx)
	if err != nil {
		return replication.PermitState{}, err
	}
	return r.ReadReplicaPermit(ctx, id)
}
func (p lazyHostPermits) ReadWriterPermits(ctx context.Context, id string) ([]replication.PermitState, error) {
	r, err := p.reader(ctx)
	if err != nil {
		return nil, err
	}
	return r.ReadWriterPermits(ctx, id)
}
func (p lazyHostPermits) ReadCredentialEnvironment(ctx context.Context, path string) ([]string, error) {
	r, err := p.reader(ctx)
	if err != nil {
		return nil, err
	}
	return r.ReadCredentialEnvironment(ctx, path)
}
