//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

const engineNativePath = "/opt/spacetime/spacetimedb-standalone"
const privateEnginePort = 23991

type engineReplayInput struct {
	Runtime data.EngineRuntime `json:"runtime"`
	Token   string             `json:"publisher_token"`
}

type engineReplayObservation struct {
	Database      data.NativeDatabaseIdentity  `json:"database"`
	Publisher     data.NativePublisherIdentity `json:"publisher"`
	Names         []string                     `json:"names"`
	Program       data.NativeProgramWireHash   `json:"program_wire_hash"`
	Canonicalizer string                       `json:"canonicalizer"`
	SchemaSHA256  string                       `json:"schema_sha256"`
}

func (o engineReplayObservation) matches(runtime data.EngineRuntime) bool {
	return o.Database == runtime.Database && o.Publisher == runtime.Publisher && slices.Equal(o.Names, runtime.Names) && o.Program == runtime.Module.Program && o.Canonicalizer == runtime.Module.Schema.Canonicalizer && o.SchemaSHA256 == runtime.Module.Schema.SHA256
}

// RunEngineCopyObserver runs only in the owned copy-only container. Its private
// stdin is separate from the public request protocol; credentials never return.
func RunEngineCopyObserver(ctx context.Context, input io.Reader, output io.Writer) error {
	if err := engineReplayContainment(); err != nil {
		return err
	}
	request, err := readEngineReplayInput(input)
	if err != nil {
		return err
	}
	defer func() { request.Token = "" }()
	root, err := os.OpenRoot("/data")
	if err != nil {
		return errEngineSource
	}
	disabled := engineModuleHTTPDisabled(root)
	_ = root.Close()
	if !disabled {
		return errEngineSource
	}
	credential, err := spacetimedb.NewPublisherCredential([]byte(request.Token))
	if err != nil {
		return errEngineSource
	}
	expected, err := spacetimedb.NewExpectedDatabase(string(request.Runtime.Database), string(request.Runtime.Publisher), request.Runtime.Names)
	if err != nil {
		return errEngineSource
	}
	endpoint, err := spacetimedb.NewPrivateEndpoint("127.0.0.1", privateEnginePort, string(request.Runtime.Database))
	if err != nil {
		return errEngineSource
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(request.Runtime.StartupSeconds)*time.Second)
	defer cancel()
	command, err := localexec.StartForeground(ctx, localexec.ForegroundCommand{Path: engineNativePath, Args: engineNativeArguments(request.Runtime, privateEnginePort), Env: []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "TMPDIR=/tmp", "TOKIO_WORKER_THREADS=2"}, Dir: "/tmp"})
	if err != nil {
		return errEngineSource
	}
	exited := command.Done()
	observed, observeErr := waitEngineObservation(ctx, endpoint, credential, expected)
	stopErr := stopEngineChild(ctx, command, exited, time.Duration(request.Runtime.StopSeconds)*time.Second)
	if observeErr != nil || stopErr != nil || ctx.Err() != nil || !engineReplayAlone(request.Runtime.PIDsLimit) {
		return errEngineSource
	}
	result := engineReplayObservation{Database: data.NativeDatabaseIdentity(observed.Database()), Publisher: data.NativePublisherIdentity(observed.Publisher()), Names: observed.Names(), Program: data.NativeProgramWireHash(observed.CurrentModule()), Canonicalizer: observed.Schema().Version(), SchemaSHA256: observed.Schema().SHA256()}
	if !result.matches(request.Runtime) {
		return errEngineSource
	}
	return json.NewEncoder(output).Encode(result)
}

func readEngineReplayInput(input io.Reader) (engineReplayInput, error) {
	raw, err := io.ReadAll(io.LimitReader(input, (80<<10)+1))
	if err != nil || len(raw) > 80<<10 {
		return engineReplayInput{}, errEngineSource
	}
	defer clear(raw)
	fields, err := strictjson.Object(raw, "runtime", "publisher_token")
	if err != nil {
		return engineReplayInput{}, errEngineSource
	}
	runtime, err := strictjson.Value[data.EngineRuntime](fields["runtime"])
	if err != nil || runtime.State != data.EngineInitialized || !runtime.SecretsAdmit(time.Now().UTC(), time.Duration(runtime.StartupSeconds)*time.Second) {
		return engineReplayInput{}, errEngineSource
	}
	token, err := strictjson.Value[string](fields["publisher_token"])
	if err != nil || len(token) > 16<<10 {
		return engineReplayInput{}, errEngineSource
	}
	return engineReplayInput{Runtime: runtime, Token: token}, nil
}

func engineReplayContainment() error {
	if os.Getpid() != 1 || os.Geteuid() != 1000 || os.Getegid() != 1000 {
		return errEngineSource
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) != 1 || interfaces[0].Flags&net.FlagLoopback == 0 {
		return errEngineSource
	}
	for _, path := range []string{"/work", "/home/brine", "/run/podman/podman.sock", "/var/run/docker.sock", "/data/program-bytes"} {
		if _, err = os.Lstat(path); !os.IsNotExist(err) {
			return errEngineSource
		}
	}
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil || !engineReplayMounts(raw) {
		return errEngineSource
	}
	status, err := engineProcessStatus("/proc/self")
	if err != nil || !engineProcessOwned(status) || !slices.Equal(strings.Fields(status["NSpid"]), []string{"1"}) {
		return errEngineSource
	}
	return nil
}

func engineReplayMounts(raw []byte) bool {
	if len(raw) > 64<<10 {
		return false
	}
	required := map[string]string{"/": "ro", "/data": "rw", "/keys/private.pem": "ro", "/keys/public.pem": "ro", "/brine": "ro", "/proc": "rw", "/sys": "ro", "/sys/fs/cgroup": "ro", "/tmp": "rw"}
	allowed := map[string]bool{}
	for _, path := range []string{"/dev", "/dev/pts", "/dev/shm", "/dev/mqueue", "/dev/null", "/dev/zero", "/dev/full", "/dev/tty", "/dev/random", "/dev/urandom", "/run", "/var/tmp", "/run/.containerenv", "/etc/hostname", "/etc/hosts", "/etc/resolv.conf", "/proc/keys", "/proc/latency_stats", "/proc/timer_list", "/proc/asound", "/proc/bus", "/proc/fs", "/proc/irq", "/proc/sys", "/proc/sysrq-trigger", "/sys/firmware"} {
		allowed[path] = true
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		before, _, found := strings.Cut(line, " - ")
		fields := strings.Fields(before)
		if !found || len(fields) < 6 || seen[fields[4]] {
			return false
		}
		path := fields[4]
		mode, mandatory := required[path]
		if !mandatory && !allowed[path] || mandatory && !slices.Contains(strings.Split(fields[5], ","), mode) {
			return false
		}
		seen[path] = true
	}
	for path := range required {
		if !seen[path] {
			return false
		}
	}
	return true
}

func engineNativeArguments(runtime data.EngineRuntime, port uint16) []string {
	return []string{"start", "--listen-addr", "127.0.0.1:" + strconv.FormatUint(uint64(port), 10), "--data-dir", "/data", "--jwt-pub-key-path", "/keys/public.pem", "--jwt-priv-key-path", "/keys/private.pem", "--page_pool_max_size", strconv.FormatInt(int64(runtime.PagePoolMB)<<20, 10), "--non-interactive"}
}

func waitEngineObservation(ctx context.Context, endpoint spacetimedb.PrivateEndpoint, credential spacetimedb.PublisherCredential, expected spacetimedb.ExpectedDatabase) (spacetimedb.Observation, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(privateEnginePort))
		if err == nil {
			_ = connection.Close()
			return spacetimedb.ObserveCurrent(ctx, endpoint, credential, expected, spacetimedb.DefaultObserverLimits())
		}
		select {
		case <-ctx.Done():
			return spacetimedb.Observation{}, errEngineSource
		case <-ticker.C:
		}
	}
}

func engineReplayAlone(limit int) bool {
	proc, err := os.Open("/proc")
	if err != nil {
		return false
	}
	defer func() { _ = proc.Close() }()
	names, err := proc.Readdirnames(limit + 512)
	if !errors.Is(err, io.EOF) && err != nil || len(names) == limit+512 {
		return false
	}
	for _, name := range names {
		pid, err := strconv.ParseUint(name, 10, 32)
		if err == nil && pid != 1 {
			return false
		}
	}
	return true
}

func stopEngineChild(ctx context.Context, command *localexec.ForegroundProcess, exited <-chan error, budget time.Duration) error {
	if command == nil {
		return errEngineSource
	}
	if err := command.Interrupt(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return engineChildStopError{phase: "interrupt", cause: errEngineSource}
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	phase := "budget"
	select {
	case err := <-exited:
		if err != nil {
			return engineChildStopError{phase: "wait", cause: err}
		}
		return nil
	case <-ctx.Done():
		phase = "context"
	case <-timer.C:
	}
	_ = command.KillGroup()
	select {
	case <-exited:
		return engineChildStopError{phase: phase, cause: errEngineSource}
	case <-time.After(time.Second):
		return engineChildStopError{phase: phase, cause: errEngineSource}
	}
}

type engineChildStopError struct {
	phase string
	cause error
}

func (e engineChildStopError) Error() string { return "native child did not stop cleanly" }
func (e engineChildStopError) Unwrap() error { return e.cause }

func decodeEngineReplayObservation(raw []byte) (engineReplayObservation, error) {
	if len(raw) > 64<<10 {
		return engineReplayObservation{}, errEngineSource
	}
	if _, err := strictjson.Object(raw, "database", "publisher", "names", "program_wire_hash", "canonicalizer", "schema_sha256"); err != nil {
		return engineReplayObservation{}, errEngineSource
	}
	var observed engineReplayObservation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observed); err != nil {
		return engineReplayObservation{}, errEngineSource
	}
	return observed, nil
}
