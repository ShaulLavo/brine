//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/podman"
)

func descriptorLeaseFixture(t *testing.T) *descriptorLease {
	t.Helper()
	preparation, binding := publicationPreparationFixture(t)
	staged, err := stagePublicationArtifact(context.Background(), preparation, binding, publicationArtifactFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = staged.discard() })
	return &descriptorLease{artifact: staged, image: podman.ImageInfo{ImageID: strings.Repeat("f", 64)}, imageRef: "controlled-image"}
}

func descriptorContainerFixture(lease *descriptorLease) engineContainer {
	nonce := lease.artifact.nonce
	container := engineContainer{ID: strings.Repeat("c", 64), Name: "brine-module-describe-" + nonce, Image: lease.image.ImageID, Path: "/brine", Args: []string{"host", "module-describe"}}
	container.Config.Labels = map[string]string{"BRINE_PRIVATE_VERIFY": nonce, "BRINE_MODULE_DESCRIPTOR": nonce}
	container.Config.User, container.Config.Umask, container.Config.StopSignal = "1000:1000", "0077", "SIGINT"
	seconds := descriptorStopSeconds
	container.Config.StopTimeout = &seconds
	container.State.Running, container.State.PID = engineBool(false), new(int)
	container.HostConfig.NetworkMode, container.HostConfig.PidMode = "none", "private"
	container.HostConfig.CgroupManager, container.HostConfig.CgroupMode = "systemd", "private"
	container.HostConfig.ReadonlyRootfs, container.HostConfig.Privileged = engineBool(true), engineBool(false)
	container.HostConfig.SecurityOpt = []string{"no-new-privileges"}
	mem, pids, cpu := int64(descriptorMemoryMB<<20), int64(descriptorPIDs), int64(1_000_000_000)
	container.HostConfig.Memory, container.HostConfig.PidsLimit, container.HostConfig.NanoCpus = &mem, &pids, &cpu
	container.Mounts = []engineMount{{Source: lease.artifact.path(), Destination: moduleDescriptorArtifactPath, Type: "bind", RW: engineBool(false)}, {Source: "/usr/local/bin/brine", Destination: "/brine", Type: "bind", RW: engineBool(false)}}
	return container
}

type descriptorContainerExecutor struct {
	container engineContainer
	removed   bool
	keep      bool
	stopErr   error
	stops     int
}

func (r *descriptorContainerExecutor) Execute(_ context.Context, command localexec.Command) (localexec.Result, error) {
	args := command.Args
	if slices.Equal(args, []string{"ps", "--all", "--quiet", "--no-trunc"}) {
		if r.removed {
			return localexec.Result{}, nil
		}
		return localexec.Result{Stdout: r.container.ID}, nil
	}
	if slices.Equal(args, []string{"container", "inspect", r.container.ID}) {
		raw, err := json.Marshal([]engineContainer{r.container})
		return localexec.Result{Stdout: string(raw)}, err
	}
	if len(args) == 5 && args[0] == "container" && args[1] == "inspect" && args[2] == "--format" {
		return localexec.Result{Stdout: "536870912\n"}, nil
	}
	if slices.Equal(args, []string{"stop", "--time", "2", r.container.ID}) {
		r.stops++
		if r.stopErr != nil {
			return localexec.Result{}, r.stopErr
		}
		*r.container.State.Running, *r.container.State.PID = false, 0
		return localexec.Result{}, nil
	}
	if slices.Equal(args, []string{"container", "rm", r.container.ID}) {
		r.removed = !r.keep
		return localexec.Result{}, nil
	}
	return localexec.Result{}, errors.New("unexpected descriptor effect")
}

func descriptorSession(t *testing.T, executor *descriptorContainerExecutor) localexec.Session {
	t.Helper()
	session, err := localexec.NewSession(executor, uint32(os.Geteuid()), t.TempDir(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestDescriptorOuterCannotAcceptMetadataWithoutActualProcessBinding(t *testing.T) {
	lease := descriptorLeaseFixture(t)
	raw, err := os.ReadFile("../spacetimedb/testdata/descriptor-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	runner := &descriptorTestRunner{output: localexec.Output{Stdout: raw}}
	executor := &descriptorContainerExecutor{container: descriptorContainerFixture(lease)}
	_, retained, err := extractPublicationCandidate(context.Background(), lease, runner, descriptorSession(t, executor))
	if !errors.Is(err, datapublish.ErrRecord) || len(retained) != 0 || runner.command.Path != "" {
		t.Fatal("synthetic container without actual process granted metadata", err)
	}
	args := descriptorArguments(lease)
	if args[0] != "create" || args[len(args)-1] != "module-describe" || !slices.Contains(args, "--interactive") || !slices.Contains(args, lease.artifact.path()+":"+moduleDescriptorArtifactPath+":ro") {
		t.Fatal("gated descriptor purpose lost its fixed artifact-only launch")
	}
}

func TestDescriptorContainerInspectionCannotReplaceLiveProcBinding(t *testing.T) {
	lease := descriptorLeaseFixture(t)
	container := descriptorContainerFixture(lease)
	*container.State.Running, *container.State.PID = true, os.Getpid()
	container.State.CgroupPath = "/unrelated-scope"
	if handle, err := bindDescriptorProcess(lease, container); !errors.Is(err, datapublish.ErrRecord) || handle != nil {
		t.Fatal("caller-selected PID replaced independently owned cgroup", err)
	}
	if descriptorProcessDisposed(context.Background(), nil) != datapublish.ErrRecord {
		t.Fatal("missing pidfd granted process disappearance")
	}
}

func TestDescriptorArtifactBindingUsesRetainedInodeInsteadOfCurrentPath(t *testing.T) {
	lease := descriptorLeaseFixture(t)
	path := lease.artifact.path()
	original, err := os.Stat(path)
	if err != nil || !descriptorArtifactIdentity(original, lease.artifact.member) {
		t.Fatal("owned staged inode lost", err)
	}
	if err = os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, lease.artifact.artifact.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if descriptorArtifactIdentity(replacement, lease.artifact.member) {
		t.Fatal("matching bytes at a replacement path replaced retained inode authority")
	}
}

func TestDescriptorOuterCannotAdoptUnknownOrRemainingProcess(t *testing.T) {
	for _, attack := range []string{"extra-mount", "foreign-image", "writable-artifact", "remain-after-remove", "unknown-stop", "missing-container"} {
		t.Run(attack, func(t *testing.T) { testDescriptorDisposalAttack(t, attack) })
	}
}

func testDescriptorDisposalAttack(t *testing.T, attack string) {
	t.Helper()
	lease := descriptorLeaseFixture(t)
	executor := &descriptorContainerExecutor{container: descriptorContainerFixture(lease)}
	switch attack {
	case "extra-mount":
		executor.container.Mounts = append(executor.container.Mounts, engineMount{Source: "/private/source", Destination: "/var/tmp", Type: "bind", RW: engineBool(false)})
	case "foreign-image":
		executor.container.Image = strings.Repeat("a", 64)
	case "writable-artifact":
		executor.container.Mounts[0].RW = engineBool(true)
	case "remain-after-remove":
		executor.keep = true
	case "unknown-stop":
		*executor.container.State.Running, *executor.container.State.PID = true, 123
		executor.stopErr = &localexec.Error{Kind: localexec.UnknownOutcome}
	case "missing-container":
		executor.removed = true
	}
	if err := disposeDescriptorProcess(context.Background(), lease, descriptorSession(t, executor)); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("unknown, absent or unowned container granted disposal proof", attack, err)
	}
	if executor.stops > 1 {
		t.Fatal("unknown descriptor stop replayed")
	}
}
