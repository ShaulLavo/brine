package readiness

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

func reviewPolicy(t *testing.T, domains, prefixes string) policy.Policy {
	t.Helper()
	raw := fmt.Sprintf("schema_version=1\nversion=\"fixture\"\nallowed_domains=%s\n[resources]\nmemory_mb=512\npids_limit=128\n[[allowed_registries]]\nhost=\"registry.example.com\"\nrepository_prefixes=%s\n", domains, prefixes)
	p, err := policy.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestReviewWildcardPolicyStillHasDecodableReport(t *testing.T) {
	p := reviewPolicy(t, `["*.example.com","app.example.com"]`, `["approved/repo"]`)
	n := &scopeNetwork{}
	r, err := (Reader{Policy: policyFixture{p}, Network: n, Store: scopeStore{}}).Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r)
	_, err = DecodeReport(raw)
	t.Logf("domains=%+v calls=%v decode=%v", r.Domains, n.calls, err)
	if err != nil {
		t.Fatal("supported wildcard policy makes readiness report undecodable")
	}
	for _, call := range n.calls {
		if call == "dns:*.example.com" || call == "tls:*.example.com" {
			t.Error("wildcard sent to exact-domain network interface")
		}
	}
}
func TestReviewEmptyRegistryPrefixesDenyDeploy(t *testing.T) {
	p := reviewPolicy(t, `["app.example.com"]`, `[]`)
	r := readyReport()
	reader := Reader{Policy: policyFixture{p}, Network: &scopeNetwork{}}
	reader.policy(t.Context(), &r, target.Snapshot{})
	Evaluate(&r)
	t.Logf("policy=%+v registries=%v ready=%s findings=%v", *r.Facts.Policy.Value, r.Registries, r.Ready, r.Findings)
	if r.Facts.Policy.Value.State != "denies_deploy" || r.Ready != NotReady {
		t.Fatal("policy permits no repositories but reports ready")
	}
}
func TestReviewDockerHubUsesExistingOCIEndpoint(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"docker.io", "registry-1.docker.io"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "registry-1.docker.io" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(404)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	defer server.Close()
	var addresses []string
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		addresses = append(addresses, address)
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	n := NetworkProbes{roots: roots, dial: dial}
	control, err := n.Registry(t.Context(), "registry-1.docker.io")
	if err != nil || !control {
		t.Fatal("TLS /v2/ control failed", control, err)
	}
	reachable, err := n.Registry(t.Context(), "docker.io")
	t.Logf("control_endpoint=%t policy_alias=%t destinations=%v err=%v", control, reachable, addresses, err)
	if err != nil || !reachable {
		t.Fatal("readiness probes website authority instead of existing Docker Hub OCI endpoint")
	}
}

func TestReadinessUsableRegistryGrantMatrix(t *testing.T) {
	for _, test := range []struct {
		name, registries, want string
	}{
		{"none", "", "denies_deploy"},
		{"omitted prefixes", "[[allowed_registries]]\nhost=\"registry.example.com\"\n", "denies_deploy"},
		{"empty prefixes", "[[allowed_registries]]\nhost=\"registry.example.com\"\nrepository_prefixes=[]\n", "denies_deploy"},
		{"mixed", "[[allowed_registries]]\nhost=\"empty.example.com\"\nrepository_prefixes=[]\n[[allowed_registries]]\nhost=\"registry.example.com\"\nrepository_prefixes=[\"approved/repo\"]\n", "valid"},
	} {
		p, err := policy.Parse([]byte("schema_version=1\nversion=\"fixture\"\nallowed_domains=[\"app.example.com\"]\n[resources]\nmemory_mb=512\npids_limit=128\n" + test.registries))
		if err != nil {
			t.Fatal(test.name, err)
		}
		r := readyReport()
		reader := Reader{Policy: policyFixture{p}, Network: &scopeNetwork{}}
		reader.policy(t.Context(), &r, target.Snapshot{})
		if r.Facts.Policy.Value == nil || r.Facts.Policy.Value.State != test.want {
			t.Fatal(test.name, r.Facts.Policy)
		}
	}
}

func TestReadinessWildcardCoverageIsExplicitlyUnprobed(t *testing.T) {
	p := reviewPolicy(t, `["*.example.com"]`, `["approved/repo"]`)
	n := &scopeNetwork{}
	r, err := (Reader{Policy: policyFixture{p}, Network: n, Store: scopeStore{}}).Read(t.Context())
	if err != nil || !r.Truncated || len(r.Domains) != 0 {
		t.Fatal(r, err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReport(raw); err != nil {
		t.Fatal(err)
	}
	for _, call := range n.calls {
		if call != "registry:registry.example.com" {
			t.Fatal("wildcard became an exact probe", call)
		}
	}
}
