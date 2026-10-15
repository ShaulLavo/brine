//go:build linux

package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sys/unix"
)

type engineMaterial struct {
	publisher  []byte
	privateKey string
	publicKey  string
}

func (m *engineMaterial) clear() { clear(m.publisher); m.publisher = nil }

func engineMaterialPaths(stateRoot string, runtime data.EngineRuntime) engineMaterial {
	base := filepath.Join(stateRoot, "engine-secrets", string(runtime.BindingID))
	return engineMaterial{
		privateKey: filepath.Join(base, "signing-private", "v"+strconv.FormatUint(runtime.Secrets.SigningPrivate.Version, 10)+".secret"),
		publicKey:  filepath.Join(base, "signing-public", "v"+strconv.FormatUint(runtime.Secrets.SigningPublic.Version, 10)+".secret"),
	}
}

func readEngineMaterial(ctx context.Context, stateRoot string, permit store.EnginePermit) (engineMaterial, error) {
	root, err := engineOpenRoot(stateRoot)
	if err != nil {
		return engineMaterial{}, errEngineSource
	}
	defer func() { _ = root.Close() }()
	secrets := permit.Runtime.Secrets
	base := filepath.Join("engine-secrets", string(permit.Runtime.BindingID))
	roles := []string{"signing-private", "signing-public", "auth-policy"}
	versions := []data.EngineSecretVersion{secrets.SigningPrivate, secrets.SigningPublic, secrets.AuthPolicy}
	values := make([][]byte, len(roles))
	defer func() {
		for _, value := range values {
			clear(value)
		}
	}()
	for i, role := range roles {
		name := filepath.Join(base, role, "v"+strconv.FormatUint(versions[i].Version, 10)+".secret")
		values[i], err = readEnginePrivateFile(ctx, root, name, versions[i].SHA256, 32<<10)
		if err != nil {
			return engineMaterial{}, err
		}
	}
	if len(values[0]) == 0 || len(values[1]) == 0 || !engineAuthPolicy(values[2]) {
		return engineMaterial{}, errEngineSource
	}
	return engineMaterialPaths(stateRoot, permit.Runtime), nil
}

func readPublisherMaterial(ctx context.Context, stateRoot string, permit store.EnginePermit) (engineMaterial, error) {
	budget := time.Duration(permit.Runtime.StartupSeconds+permit.Runtime.StopSeconds+35) * time.Second
	if !permit.Runtime.PublishingSecretsAdmit(time.Now().UTC(), budget) {
		return engineMaterial{}, errEngineSource
	}
	material, err := readEngineMaterial(ctx, stateRoot, permit)
	if err != nil {
		return engineMaterial{}, err
	}
	root, err := engineOpenRoot(stateRoot)
	if err != nil {
		return engineMaterial{}, errEngineSource
	}
	defer func() { _ = root.Close() }()
	secret := permit.Runtime.Secrets.Publisher
	name := filepath.Join("engine-secrets", string(permit.Runtime.BindingID), "publisher", "v"+strconv.FormatUint(secret.Version, 10)+".secret")
	material.publisher, err = readEnginePrivateFile(ctx, root, name, secret.SHA256, 16<<10)
	if err != nil || len(material.publisher) == 0 {
		material.clear()
		return engineMaterial{}, errEngineSource
	}
	return material, nil
}

func readEnginePrivateFile(ctx context.Context, root *os.Root, name, expected string, limit int64) ([]byte, error) {
	if ctx.Err() != nil || engineDirectoryChain(root, filepath.Dir(name)) != nil {
		return nil, errEngineSource
	}
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() < 0 || info.Size() > limit {
		return nil, errEngineSource
	}
	file, err := engineOpenMember(root, name, unix.O_RDONLY, 0)
	if err != nil {
		return nil, errEngineSource
	}
	defer func() { _ = file.Close() }()
	member, err := engineMember(file, name)
	if err != nil || member.Size != info.Size() {
		return nil, errEngineSource
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) != member.Size || ctx.Err() != nil {
		clear(raw)
		return nil, errEngineSource
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != expected {
		clear(raw)
		return nil, errEngineSource
	}
	return raw, nil
}

func engineAuthPolicy(raw []byte) bool {
	fields, err := strictjson.Object(raw, "mode", "require_client_token")
	if err != nil {
		return false
	}
	mode, err := strictjson.Value[string](fields["mode"])
	if err != nil || mode != "supplied_key_jwt" {
		return false
	}
	required, err := strictjson.Value[bool](fields["require_client_token"])
	return err == nil && required
}

func engineModuleHTTPDisabled(root *os.Root) bool {
	file, err := engineOpenMember(root, "config.toml", unix.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return false
	}
	var config struct {
		ModuleHTTP struct {
			Enabled *bool `toml:"enabled"`
		} `toml:"module-http"`
	}
	return toml.Unmarshal(raw, &config) == nil && config.ModuleHTTP.Enabled != nil && !*config.ModuleHTTP.Enabled
}

func verifyEngineService(ctx context.Context, home string, permit store.EnginePermit) error {
	root, err := engineOpenRoot(filepath.Join(home, ".config", "systemd", "user"))
	if err != nil {
		return errEngineSource
	}
	defer func() { _ = root.Close() }()
	name := "brine-engine-" + string(permit.Runtime.BindingID) + ".service"
	raw, err := readEnginePrivateFile(ctx, root, name, permit.Runtime.UnitSHA256, 64<<10)
	if err != nil {
		return err
	}
	want, err := EngineServiceBytes(permit.Runtime)
	if err != nil || string(raw) != string(want) {
		return errEngineSource
	}
	return nil
}

func sameEnginePermit(first, second store.EnginePermit) bool {
	a, err := json.Marshal(first)
	if err != nil {
		return false
	}
	b, err := json.Marshal(second)
	return err == nil && string(a) == string(b)
}

func engineMountPath(path string) bool {
	return data.ValidRoot(path) && !strings.ContainsAny(path, ":\x00\r\n")
}
