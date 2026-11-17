package dataplane_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tsic404/kos-filemanager/pkg/dataplane"
)

// digestOf is the digest the window host would publish for one token value.
func digestOf(t *testing.T, value string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// writeTable publishes one digest table below a temporary directory.
func writeTable(t *testing.T, entries ...map[string]any) string {
	t.Helper()
	directory := t.TempDir()
	document := map[string]any{"tokens": entries}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode the table: %v", err)
	}
	path := filepath.Join(directory, "tokens.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write the table: %v", err)
	}
	return path
}

// TestFromEnvironmentRefusesAMissingInjection proves the process does not start
// with an authorization input left out.
func TestFromEnvironmentRefusesAMissingInjection(t *testing.T) {
	_, err := dataplane.FromEnvironment(func(name string) string {
		switch name {
		case dataplane.EnvAddr:
			return ":9000"
		default:
			return ""
		}
	})
	if err == nil {
		t.Fatal("a deployment without any injection started")
	}
	for _, name := range []string{dataplane.EnvVolumeRoot, dataplane.EnvTokenTable} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not name the missing %s", err, name)
		}
	}
}

// httptestServer serves one handler for a case.
func httptestServer(handler http.Handler) *httptest.Server {
	return httptest.NewServer(handler)
}

// get sends one request and decodes the answer.
func get(t *testing.T, url string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send the request: %v", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	decoded := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
	}
	return response.StatusCode, decoded
}

// kubeconfigOf writes a minimal kubeconfig a client can be built from. The
// address is never dialed by the cases that only assert the assembly.
func kubeconfigOf(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig")
	content := `apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://127.0.0.1:1
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user:
    token: test
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the kubeconfig: %v", err)
	}
	return path
}

// TestFromEnvironmentKeepsTheInjectedValues proves a complete deployment is read
// as it was injected, and that the address falls back to the default.
func TestFromEnvironmentKeepsTheInjectedValues(t *testing.T) {
	values := map[string]string{
		dataplane.EnvVolumeRoot: "/srv/volumes",
		dataplane.EnvTokenTable: "/run/kos/tokens.json",
		dataplane.EnvKubeconfig: "/etc/kos/kubeconfig",
	}
	deployment, err := dataplane.FromEnvironment(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("read the deployment: %v", err)
	}
	if deployment.Addr != dataplane.DefaultAddr {
		t.Fatalf("address = %q, want the default %q", deployment.Addr, dataplane.DefaultAddr)
	}
	if deployment.VolumeRoot != values[dataplane.EnvVolumeRoot] || deployment.TokenTable != values[dataplane.EnvTokenTable] {
		t.Fatalf("deployment = %+v, want the injected values", deployment)
	}
}

// TestNewStandaloneRefusesUnusableInputs proves a path that cannot be served
// stops the process, rather than surfacing as a request-time refusal.
func TestNewStandaloneRefusesUnusableInputs(t *testing.T) {
	root := t.TempDir()
	table := writeTable(t)
	for name, deployment := range map[string]dataplane.Deployment{
		"no volume root":     {TokenTable: table},
		"no token table":     {VolumeRoot: root},
		"missing root":       {VolumeRoot: filepath.Join(root, "absent"), TokenTable: table},
		"root is a file":     {VolumeRoot: table, TokenTable: table},
		"table is a dir":     {VolumeRoot: root, TokenTable: root},
		"missing kubeconfig": {VolumeRoot: root, TokenTable: table, Kubeconfig: filepath.Join(root, "absent")},
	} {
		if _, err := dataplane.NewStandalone(deployment); err == nil {
			t.Fatalf("%s started", name)
		}
	}
}

// TestTokenTableResolvesLiveDigests proves the file-backed verifier answers the
// lifecycle the window host publishes: a live digest resolves, an expired one
// and an unknown value do not, and a table rewritten by the host is read again
// on the next request (KOSD-C-75).
func TestTokenTableResolvesLiveDigests(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	live := "live-token-value"
	path := writeTable(t, map[string]any{
		"digest":    digestOf(t, live),
		"appId":     "kos-filemanager",
		"account":   "alice",
		"windowId":  "window-1",
		"expiresAt": now.Add(10 * time.Minute),
	})
	table := dataplane.TokenTable{Path: path, Now: func() time.Time { return now }}
	binding, err := table.Authenticate(live)
	if err != nil {
		t.Fatalf("authenticate a live token: %v", err)
	}
	if binding.Account != "alice" || binding.WindowID != "window-1" {
		t.Fatalf("binding = %+v, want the published entry", binding)
	}
	if _, err := table.Authenticate("unknown-value"); err == nil {
		t.Fatal("an unpublished value authenticated")
	}
	// The host closes the window and publishes an empty table: the same value
	// stops resolving on the next request.
	if err := os.WriteFile(path, []byte(`{"tokens":[]}`), 0o600); err != nil {
		t.Fatalf("rewrite the table: %v", err)
	}
	if _, err := table.Authenticate(live); err == nil {
		t.Fatal("a token the host revoked still authenticated")
	}
}

// TestStandaloneServesHealthAndRefusesWithoutAToken proves the assembled process
// answers the manifest's health address and still refuses an unauthenticated
// read (KOSD-C-73, FILE-C-1).
func TestStandaloneServesHealthAndRefusesWithoutAToken(t *testing.T) {
	root := t.TempDir()
	path := writeTable(t, map[string]any{
		"digest":    digestOf(t, "live-token-value"),
		"appId":     "kos-filemanager",
		"account":   "alice",
		"windowId":  "window-1",
		"expiresAt": time.Now().Add(10 * time.Minute),
	})
	service, err := dataplane.NewStandalone(dataplane.Deployment{
		VolumeRoot: root, TokenTable: path, Kubeconfig: kubeconfigOf(t),
	})
	if err != nil {
		t.Fatalf("assemble the standalone data plane: %v", err)
	}
	server := httptestServer(service.Handler())
	defer server.Close()
	status, body := get(t, server.URL+"/healthz", nil)
	if status != http.StatusOK {
		t.Fatalf("health = %d %v, want 200", status, body)
	}
	status, body = get(t, server.URL+"/files/window", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("window without a token = %d %v, want 401", status, body)
	}
	status, body = get(t, server.URL+"/files/window", map[string]string{
		"Authorization": "Bearer live-token-value",
	})
	if status == http.StatusUnauthorized {
		t.Fatalf("a published token was refused: %d %v", status, body)
	}
}
