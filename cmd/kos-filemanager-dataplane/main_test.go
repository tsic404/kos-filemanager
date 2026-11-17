package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/tsic404/kosd/tests/acceptance/harness"
)

// binary builds this command once per case and returns its path.
func binary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kos-filemanager-dataplane")
	build := exec.Command("go", "build", "-o", path, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the command: %v\n%s", err, output)
	}
	return path
}

// TestProcessRefusesToStartWithoutInjection proves the process exits instead of
// serving with an authorization input left out.
func TestProcessRefusesToStartWithoutInjection(t *testing.T) {
	command := exec.Command(binary(t))
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		t.Fatal("the process started without any injected input")
	}
	if !strings.Contains(stderr.String(), "refusing to start") {
		t.Fatalf("stderr = %q, want a refusal", stderr.String())
	}
	for _, name := range []string{"KOS_FILEMANAGER_VOLUME_ROOT", "KOS_FILEMANAGER_TOKEN_TABLE"} {
		if !strings.Contains(stderr.String(), name) {
			t.Fatalf("stderr = %q, does not name %s", stderr.String(), name)
		}
	}
}

// TestProcessServesAnAuthorizedVolume starts the built command against a real
// API server, a real volume directory and a published token table, then reads
// the volume list the way a window does (FILE-C-1, KOSD-C-75).
func TestProcessServesAnAuthorizedVolume(t *testing.T) {
	cluster := harness.Start(t)
	cluster.Create(t, harness.Volume(harness.Namespace, "alice-data", map[string]any{
		"owner": "alice", "capacity": "1Gi",
	}))
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, harness.Namespace, "alice-data"), 0o755); err != nil {
		t.Fatalf("create the volume directory: %v", err)
	}
	value := "delivered-token-value"
	sum := sha256.Sum256([]byte(value))
	tablePath := filepath.Join(t.TempDir(), "tokens.json")
	encoded, err := json.Marshal(map[string]any{"tokens": []map[string]any{{
		"digest":    hex.EncodeToString(sum[:]),
		"appId":     "kos-filemanager",
		"account":   "alice",
		"windowId":  "window-1",
		"expiresAt": time.Now().Add(10 * time.Minute),
	}}})
	if err != nil {
		t.Fatalf("encode the token table: %v", err)
	}
	if err := os.WriteFile(tablePath, encoded, 0o600); err != nil {
		t.Fatalf("write the token table: %v", err)
	}
	address := freeAddress(t)
	command := exec.Command(binary(t))
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"KOS_FILEMANAGER_ADDR=" + address,
		"KOS_FILEMANAGER_VOLUME_ROOT=" + root,
		"KOS_FILEMANAGER_TOKEN_TABLE=" + tablePath,
		"KOS_FILEMANAGER_KUBECONFIG=" + kubeconfigOf(t, cluster),
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start the process: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	base := "http://" + address
	waitForHealth(t, base+"/healthz", &stderr)
	status, body := call(t, base+"/files/window", value)
	if status != http.StatusOK {
		t.Fatalf("window = %d %v, want 200", status, body)
	}
	items, _ := body["volumes"].([]any)
	if len(items) != 1 {
		t.Fatalf("volumes = %v, want the account's one volume", body["volumes"])
	}
	entry, _ := items[0].(map[string]any)
	if entry["name"] != "alice-data" {
		t.Fatalf("volume = %v, want alice-data", entry)
	}
	// A value the host never published stays unauthenticated.
	if status, _ := call(t, base+"/files/window", "forged-value"); status != http.StatusUnauthorized {
		t.Fatalf("forged token = %d, want 401", status)
	}
}

// waitForHealth polls the health route until the process answers or it exits.
func waitForHealth(t *testing.T, url string, stderr *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the process never answered %s: %s", url, stderr.String())
}

// call sends one window read with a bearer token.
func call(t *testing.T, url, token string) (int, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send the request: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	decoded := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
	}
	return response.StatusCode, decoded
}

// kubeconfigOf writes the test cluster's credentials as the file the process
// reads, so the deployment injects cluster access the way a manifest does.
func kubeconfigOf(t *testing.T, cluster *harness.Cluster) string {
	t.Helper()
	config := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"test": {Server: cluster.Config.Host, CertificateAuthorityData: cluster.Config.CAData},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"test": {Token: cluster.Config.BearerToken, ClientCertificateData: cluster.Config.CertData,
				ClientKeyData: cluster.Config.KeyData},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"test": {Cluster: "test", AuthInfo: "test"},
		},
		CurrentContext: "test",
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(config, path); err != nil {
		t.Fatalf("write the kubeconfig: %v", err)
	}
	return path
}

// freeAddress reserves one loopback port for a case's process.
func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve an address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release the address: %v", err)
	}
	return address
}
