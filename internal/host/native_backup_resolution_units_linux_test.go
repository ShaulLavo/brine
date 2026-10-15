//go:build linux

package host

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/store"
)

type nativeAbsentManager struct {
	key, value string
	queries    []string
}

func (r *nativeAbsentManager) Execute(_ context.Context, c localexec.Command) (localexec.Result, error) {
	if c.Path != "/usr/bin/systemctl" || c.Mutation || len(c.Args) < 4 || c.Args[1] != "show" {
		return localexec.Result{}, fmt.Errorf("absence check attempted other operation")
	}
	name := c.Args[2]
	r.queries = append(r.queries, name)
	values := map[string]string{"LoadState": "not-found", "ActiveState": "inactive", "FragmentPath": "", "UnitFileState": "", "Job": ""}
	if filepath.Ext(name) == ".service" {
		values["MainPID"], values["ControlPID"], values["ControlGroup"] = "0", "0", ""
	}
	if r.key != "" {
		values[r.key] = r.value
	}
	var out strings.Builder
	for key, value := range values {
		fmt.Fprintf(&out, "%s=%s\n", key, value)
	}
	return localexec.Result{Stdout: out.String()}, nil
}

func nativeBackupAbsenceFixture(t *testing.T, r *nativeAbsentManager) (nativeBackupResolutionOperations, store.NativeBackupActivationMetadata, string) {
	t.Helper()
	_, home, _ := managedUnitsFixture(t)
	if err := os.Chmod(home, 0755); err != nil { //nolint:gosec // Actual enrolled home permission boundary.
		t.Fatal(err)
	}
	session, err := localexec.NewSession(r, nativeBackupFixtureUID(t), home, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	metadata := store.NativeBackupActivationMetadata{Plan: store.NativeBackupRevisionPlan{BindingID: data.ReplicaBindingID(strings.Repeat("a", 32)), Backup: policy.SpacetimePolicy{CaptureIntervalSeconds: 37}}}
	return nativeBackupResolutionOperations{runtime: nativeBackupRuntime{home: home, session: session}}, metadata, home
}

func TestNativeBackupIssuedUnitAbsenceRequiresExactManagerAndFiles(t *testing.T) {
	r := &nativeAbsentManager{}
	operation, metadata, home := nativeBackupAbsenceFixture(t, r)
	ctx := context.Background()
	if err := operation.absentUnits(ctx, metadata); err != nil {
		t.Fatal("actual absent units refused", err)
	}
	if len(r.queries) != 2 {
		t.Fatal("both manager units were not inspected")
	}
	for _, suffix := range []string{".service", ".timer", ".service.new", ".timer.new"} {
		path := filepath.Join(home, managedServiceDirectory, nativeBackupUnitName(metadata)+suffix)
		if err := os.WriteFile(path, []byte("foreign or partial artifact"), 0600); err != nil {
			t.Fatal(err)
		}
		if operation.absentUnits(ctx, metadata) == nil {
			t.Fatal("present artifact declared absent", suffix)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativeBackupIssuedUnitAbsenceRefusesJobsProcessesAndForeignUnits(t *testing.T) {
	for key, value := range map[string]string{"LoadState": "loaded", "ActiveState": "active", "FragmentPath": "/foreign/unit", "UnitFileState": "enabled", "Job": "9 /job/9", "MainPID": "42", "ControlPID": "43", "ControlGroup": "/foreign/cgroup"} {
		t.Run(key, func(t *testing.T) {
			operation, metadata, _ := nativeBackupAbsenceFixture(t, &nativeAbsentManager{key: key, value: value})
			if operation.absentUnits(context.Background(), metadata) == nil {
				t.Fatal("uncertain manager state admitted repair")
			}
		})
	}
}

func TestNativeBackupConfigRefusesWritableAndSymlinkBoundaries(t *testing.T) {
	for _, component := range []string{".", ".config", ".config/systemd", ".config/systemd/user"} {
		t.Run(component, func(t *testing.T) {
			operation, metadata, home := nativeBackupAbsenceFixture(t, &nativeAbsentManager{})
			if err := os.Chmod(filepath.Join(home, component), 0770); err != nil { //nolint:gosec // Deliberately unsafe ownership boundary.
				t.Fatal(err)
			}
			if operation.absentUnits(context.Background(), metadata) == nil {
				t.Fatal("writable boundary accepted")
			}
		})
	}
	_, home, _ := managedUnitsFixture(t)
	config := filepath.Join(home, ".config")
	if err := os.Rename(config, config+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(config+"-retained", config); err != nil {
		t.Fatal(err)
	}
	if root, err := nativeBackupConfigRoot(home); err == nil {
		_ = root.Close()
		t.Fatal("symlink config accepted")
	}
}

type nativeEnrolledHomeInfo struct {
	os.FileInfo
	uid uint32
}

func (i nativeEnrolledHomeInfo) Sys() any {
	value := *i.FileInfo.Sys().(*syscall.Stat_t)
	value.Uid = i.uid
	return &value
}

func TestNativeBackupHomeBoundaryAcceptsTrustedRootAndRefusesForeignOwner(t *testing.T) {
	_, home, _ := managedUnitsFixture(t)
	if err := os.Chmod(home, 0755); err != nil { //nolint:gosec // Exact enrolled home mode.
		t.Fatal(err)
	}
	info, err := os.Lstat(home)
	if err != nil {
		t.Fatal(err)
	}
	if !managedAncestorNode(nativeEnrolledHomeInfo{info, 0}, true) {
		t.Fatal("root-owned0755 enrolled home refused")
	}
	if managedAncestorNode(nativeEnrolledHomeInfo{info, nativeBackupFixtureUID(t) + 1}, true) {
		t.Fatal("foreign home owner accepted")
	}
	if os.Geteuid() != 0 && managedServiceNode(nativeEnrolledHomeInfo{info, 0}, true) {
		t.Fatal("trusted ancestor owner became private descendant owner")
	}
}

func TestNativeBackupUnitAbsenceChangesReviewedObservation(t *testing.T) {
	observed := nativeBackupObserved{repositoryID: strings.Repeat("a", 64), configSHA256: strings.Repeat("b", 64), unitsState: "issued_absent"}
	absent := observed.hash()
	observed.unitsState = "verified"
	if observed.hash() == absent {
		t.Fatal("unit effect change preserved reviewed hash")
	}
	observed.unitsState = "not_issued"
	if observed.hash() == absent {
		t.Fatal("different original intent preserved reviewed hash")
	}
}

func TestNativeBackupReactivationInspectsPredecessorUnits(t *testing.T) {
	for _, stage := range []string{"prepared", "units_issued"} {
		t.Run(stage, func(t *testing.T) { checkNativeBackupPredecessorUnits(t, stage) })
	}
}

func checkNativeBackupPredecessorUnits(t *testing.T, stage string) {
	t.Helper()
	operation, metadata, home := nativeBackupAbsenceFixture(t, &nativeAbsentManager{})
	session, err := localexec.NewSession(nativeBackupManagerFixture{home}, nativeBackupFixtureUID(t), home, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	operation.runtime.session = session
	unit, timer := nativeBackupUnitBytes(metadata)
	if err := installNativeBackupUnits(t.Context(), home, session, metadata, unit, timer); err != nil {
		t.Fatal(err)
	}
	metadata.Previous = store.NativeBackupPolicy{BindingID: metadata.Plan.BindingID, Revision: 1, Backup: metadata.Plan.Backup, UnitSHA256: nativeBackupBytesHash(unit), TimerSHA256: nativeBackupBytesHash(timer)}
	metadata.Plan.Backup.CaptureIntervalSeconds = 41
	state, err := operation.inspectActivationUnits(t.Context(), metadata, stage)
	if err != nil {
		t.Fatalf("%s reactivation refuses its exact owned predecessor: %v", stage, err)
	}
	if state == "verified" || state == "issued_absent" {
		t.Fatal("predecessor mistaken for installed replacement or absent units", state)
	}
	timerPath := filepath.Join(home, managedServiceDirectory, nativeBackupUnitName(metadata)+".timer")
	retained, err := os.ReadFile(timerPath)
	if err != nil || string(retained) != string(timer) {
		t.Fatal("inspection changed predecessor units", err)
	}
	if err := os.WriteFile(timerPath, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := operation.inspectActivationUnits(t.Context(), metadata, stage); err == nil {
		t.Fatal("foreign predecessor admitted recovery")
	}
	if err := os.WriteFile(timerPath, timer, 0600); err != nil {
		t.Fatal(err)
	}
	metadata.Previous = store.NativeBackupPolicy{}
	if _, err := operation.inspectActivationUnits(t.Context(), metadata, "prepared"); err == nil {
		t.Fatal("first activation adopted existing units")
	}
}
