package localexec

import (
	"github.com/ShaulLavo/brine/internal/runnerstorage"
	"os"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestCommandStorageProfileRejectsAmbientAndArbitraryOverride(t *testing.T) {
	t.Setenv("CONTAINERS_STORAGE_CONF", "/caller/profile")
	for _, v := range []string{"/caller/profile", runnerstorage.Path + "/..", "", runnerstorage.Path + "\n"} {
		if _, _, err := commandEnvironment([]string{"CONTAINERS_STORAGE_CONF=" + v}, false); err == nil {
			t.Fatal("caller storage path admitted")
		}
	}
	env, _, err := commandEnvironment([]string{runnerstorage.Environment}, false)
	if err != nil || !slices.Contains(env, runnerstorage.Environment) {
		t.Fatal("fixed profile override refused", err)
	}
	identity, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	env, _, err = commandEnvironment(nil, false)
	if identity.Username != "brine" && (err != nil || slices.ContainsFunc(env, func(v string) bool { return strings.HasPrefix(v, "CONTAINERS_STORAGE_CONF=") })) {
		t.Fatal("ambient or supplied session UID selected profile", err)
	}
}
