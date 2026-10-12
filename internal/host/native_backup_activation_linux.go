//go:build linux

package host

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/ops"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/store"
)

func nativeBackupActivationTask(service Service, stateRoot, home string, session localexec.Session) func(context.Context, ops.Operation) (json.RawMessage, error) {
	return func(ctx context.Context, op ops.Operation) (json.RawMessage, error) {
		activation, err := service.Store.ClaimNativeBackupActivation(ctx, op.ID)
		if err != nil {
			return nil, err
		}
		defer func() { _ = activation.Close() }()
		metadata, err := activation.Metadata(ctx)
		if err != nil {
			return nil, err
		}
		pol, err := service.Policy.Load(ctx)
		if err != nil || pol.Hash() != "sha256:"+metadata.Plan.PolicySHA256 {
			return nil, result.New(result.PolicyRefused, nil)
		}
		ctx, cancel, err := nativeTransportContext(ctx, metadata.Material.Scope)
		if err != nil {
			return nil, err
		}
		defer cancel()
		runtime := nativeBackupRuntime{state: service.Store, stateRoot: stateRoot, home: home, session: session}
		grant := ""
		if metadata.Previous.Revision == 0 {
			grant = metadata.CreationGrantSHA256
		}
		client, repository, err := runtime.repositoryMaterial(ctx, metadata.Material, activation, grant)
		if err != nil {
			return nil, err
		}
		work, err := nativeBackupActivationWorkspace(stateRoot, metadata.Plan.ID)
		if err != nil {
			return nil, err
		}
		var repositoryID string
		if metadata.Previous.Revision == 0 {
			if err := activation.BeginInitialization(ctx); err != nil {
				return nil, err
			}
			initialized, err := client.Init(ctx, repository, work)
			if err != nil {
				return nil, err
			}
			if err := activation.RecordInitialized(ctx, initialized); err != nil {
				return nil, err
			}
			repositoryID = initialized.Scope().RepositoryID
		} else {
			repositoryID, err = client.RepositoryIdentity(ctx, repository, work)
			if err != nil {
				return nil, err
			}
		}
		unit, timer := nativeBackupUnitBytes(metadata)
		if err := activation.BeginUnits(ctx); err != nil {
			return nil, err
		}
		if err := installNativeBackupUnits(ctx, home, session, metadata, unit, timer); err != nil {
			return nil, err
		}
		fresh, err := activation.Metadata(ctx)
		if err != nil || fresh.Material.CredentialReceiptID != metadata.Material.CredentialReceiptID || fresh.Material.RecoverySHA256 != metadata.Material.RecoverySHA256 {
			return nil, result.New(result.PolicyRefused, nil)
		}
		receipt, err := activation.Commit(ctx, store.NativeBackupActivationObservation{RepositoryID: repositoryID, UnitSHA256: nativeBackupBytesHash(unit), TimerSHA256: nativeBackupBytesHash(timer), RecoveryDeliveryID: metadata.Material.RecoveryDeliveryID})
		if err != nil {
			return nil, err
		}
		return json.Marshal(receipt)
	}
}

func nativeBackupActivationWorkspace(stateRoot, planID string) (string, error) {
	root, err := engineOpenRoot(stateRoot)
	if err != nil {
		return "", errEngineSource
	}
	defer func() { _ = root.Close() }()
	name := filepath.Join("native-backup-revisions", planID)
	if err := engineMakePrivateDirectories(root, name); err != nil {
		return "", err
	}
	if err := syncEngineDirectory(root, filepath.Dir(name)); err != nil {
		return "", err
	}
	return filepath.Join(stateRoot, name), nil
}

func nativeBackupBytesHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func nativeBackupUnitName(metadata store.NativeBackupActivationMetadata) string {
	return "brine-native-backup-" + string(metadata.Plan.BindingID)
}
func nativeBackupUnitBytes(metadata store.NativeBackupActivationMetadata) ([]byte, []byte) {
	name := nativeBackupUnitName(metadata)
	unit := []byte(fmt.Sprintf("[Unit]\nDescription=Brine native backup %s\n\n[Service]\nType=oneshot\nExecStart=/usr/local/bin/brine host native-capture %s\nTimeoutStartSec=30\nNoNewPrivileges=yes\nPrivateTmp=yes\n", metadata.Plan.BindingID, metadata.Plan.BindingID))
	timer := []byte(fmt.Sprintf("[Unit]\nDescription=Brine native backup cadence %s\n\n[Timer]\nOnActiveSec=%ds\nOnUnitActiveSec=%ds\nUnit=%s.service\nAccuracySec=1s\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n", metadata.Plan.BindingID, metadata.Plan.Backup.CaptureIntervalSeconds, metadata.Plan.Backup.CaptureIntervalSeconds, name))
	return unit, timer
}

func installNativeBackupUnits(ctx context.Context, home string, session localexec.Session, metadata store.NativeBackupActivationMetadata, unit, timer []byte) error {
	root, err := nativeBackupConfigRoot(home)
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = root.Close() }()
	name := nativeBackupUnitName(metadata)
	if err := engineMakePrivateDirectories(root, "systemd/user"); err != nil {
		return err
	}
	if err := replaceNativeBackupUnit(ctx, root, name+".service", unit, metadata.Previous.UnitSHA256); err != nil {
		return err
	}
	if err := replaceNativeBackupUnit(ctx, root, name+".timer", timer, metadata.Previous.TimerSHA256); err != nil {
		return err
	}
	if _, err := session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "daemon-reload"}, nil, true); err != nil {
		return err
	}
	timerName := name + ".timer"
	if _, err := session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "enable", "--now", timerName}, nil, true); err != nil {
		return err
	}
	if err := verifyNativeBackupUnit(ctx, root, name+".service", unit); err != nil {
		return err
	}
	if err := verifyNativeBackupUnit(ctx, root, name+".timer", timer); err != nil {
		return err
	}
	observed, err := session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "show", timerName, "--no-pager", "--property=LoadState", "--property=ActiveState", "--property=UnitFileState", "--property=FragmentPath"}, nil, false)
	if err != nil {
		return err
	}
	expected := map[string]string{"LoadState": "loaded", "ActiveState": "active", "UnitFileState": "enabled", "FragmentPath": filepath.Join(home, ".config/systemd/user", name+".timer")}
	return checkNativeBackupTimerProperties(observed.Stdout, expected)
}

func nativeBackupConfigRoot(home string) (*os.Root, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return nil, errManagedService
	}
	if err := managedHomeAncestors(home); err != nil {
		return nil, err
	}
	homeRoot, err := engineOpenRoot(home)
	if err != nil {
		return nil, err
	}
	defer func() { _ = homeRoot.Close() }()
	file, err := engineOpenMember(homeRoot, ".config", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, errEngineSource
	}
	defer func() { _ = file.Close() }()
	if _, err = engineMember(file, ".config"); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot("/proc/self/fd/" + strconv.FormatUint(uint64(file.Fd()), 10))
	if err != nil {
		return nil, errEngineSource
	}
	return root, nil
}

func replaceNativeBackupUnit(ctx context.Context, root *os.Root, name string, raw []byte, previous string) error {
	path := filepath.Join("systemd/user", name)
	if _, err := root.Lstat(path); err == nil {
		if previous == "" {
			return errEngineSource
		}
		existing, err := readEnginePrivateFile(ctx, root, path, previous, 16<<10)
		clear(existing)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return errEngineSource
	}
	temp := path + ".new"
	if err := engineWritePrivateVersion(ctx, root, temp, nativeBackupBytesHash(raw), raw); err != nil {
		return err
	}
	if err := root.Rename(temp, path); err != nil {
		return errEngineSource
	}
	return syncEngineDirectory(root, filepath.Dir(path))
}

func verifyNativeBackupUnit(ctx context.Context, root *os.Root, name string, expected []byte) error {
	raw, err := readEnginePrivateFile(ctx, root, filepath.Join("systemd/user", name), nativeBackupBytesHash(expected), 16<<10)
	defer clear(raw)
	if err != nil || !bytes.Equal(raw, expected) {
		return errEngineSource
	}
	return nil
}

func checkNativeBackupTimerProperties(raw string, expected map[string]string) error {
	seen := make(map[string]bool, len(expected))
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		key, value, valid := strings.Cut(line, "=")
		if !valid || seen[key] || expected[key] != value {
			return errEngineSource
		}
		seen[key] = true
	}
	if len(seen) != len(expected) {
		return errEngineSource
	}
	return nil
}
