package dataplane

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Environment variables the standalone service reads. The data plane holds its
// own cluster credentials; the console BFF never does (KOSD-C-12).
const (
	// EnvAddr is the address the service listens on.
	EnvAddr = "KOS_FILEMANAGER_ADDR"
	// EnvVolumeRoot is the directory the volume mounts live under.
	EnvVolumeRoot = "KOS_FILEMANAGER_VOLUME_ROOT"
	// EnvTokenTable is the digest table the window host publishes.
	EnvTokenTable = "KOS_FILEMANAGER_TOKEN_TABLE"
	// EnvKubeconfig is the cluster credentials file; empty means in-cluster.
	EnvKubeconfig = "KOS_FILEMANAGER_KUBECONFIG"
	// DefaultAddr is the listen address when none is injected.
	DefaultAddr = ":8080"
)

// Deployment is what one standalone process is assembled from. Every dependency
// the deployment must inject is a field: the process never falls back to a
// same-process window host or to the caller's credentials.
type Deployment struct {
	// Addr is the listen address; defaults to DefaultAddr.
	Addr string
	// VolumeRoot is the directory the volume mounts live under; required.
	VolumeRoot string
	// TokenTable is the published digest table; required.
	TokenTable string
	// Kubeconfig is the cluster credentials file; empty means in-cluster.
	Kubeconfig string
	// Log records failures; nil discards them.
	Log *slog.Logger
	// Now supplies the clock; nil reads the wall clock.
	Now func() time.Time
}

// FromEnvironment reads the deployment inputs. A required input that is missing
// is returned as one error naming every missing variable, so the process
// refuses to start instead of serving without an injection.
func FromEnvironment(getenv func(string) string) (Deployment, error) {
	deployment := Deployment{
		Addr:       strings.TrimSpace(getenv(EnvAddr)),
		VolumeRoot: strings.TrimSpace(getenv(EnvVolumeRoot)),
		TokenTable: strings.TrimSpace(getenv(EnvTokenTable)),
		Kubeconfig: strings.TrimSpace(getenv(EnvKubeconfig)),
	}
	if deployment.Addr == "" {
		deployment.Addr = DefaultAddr
	}
	var missing []string
	if deployment.VolumeRoot == "" {
		missing = append(missing, EnvVolumeRoot)
	}
	if deployment.TokenTable == "" {
		missing = append(missing, EnvTokenTable)
	}
	if len(missing) > 0 {
		return Deployment{}, fmt.Errorf("dataplane: refusing to start: %s must be injected",
			strings.Join(missing, " and "))
	}
	return deployment, nil
}

// NewStandalone builds the data plane a standalone process serves. The cluster
// access comes from the deployment, never from a window host in this process.
func NewStandalone(deployment Deployment) (*Service, error) {
	if deployment.VolumeRoot == "" {
		return nil, fmt.Errorf("dataplane: refusing to start: %s must be injected", EnvVolumeRoot)
	}
	if deployment.TokenTable == "" {
		return nil, fmt.Errorf("dataplane: refusing to start: %s must be injected", EnvTokenTable)
	}
	if err := requireDirectory(EnvVolumeRoot, deployment.VolumeRoot); err != nil {
		return nil, err
	}
	if err := requireFile(EnvTokenTable, deployment.TokenTable); err != nil {
		return nil, err
	}
	config, err := clusterConfig(deployment.Kubeconfig)
	if err != nil {
		return nil, err
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("dataplane: build the cluster client: %w", err)
	}
	return New(Config{
		Tokens:  TokenTable{Path: deployment.TokenTable, Now: deployment.Now},
		Volumes: ClusterVolumes{Dynamic: client, Now: deployment.Now},
		Roots:   DirectoryRoots{Base: deployment.VolumeRoot},
		Log:     deployment.Log,
		Now:     deployment.Now,
	})
}

// clusterConfig resolves the cluster credentials the data plane runs with.
func clusterConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("dataplane: read %s: %w", EnvKubeconfig, err)
		}
		return config, nil
	}
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("dataplane: refusing to start: no %s and no in-cluster service account: %w",
			EnvKubeconfig, err)
	}
	return config, nil
}

// requireDirectory refuses a volume root the process cannot serve.
func requireDirectory(name, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("dataplane: refusing to start: %s: %w", name, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("dataplane: refusing to start: %s is not a directory", name)
	}
	return nil
}

// requireFile refuses a token table the process cannot read at startup.
func requireFile(name, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("dataplane: refusing to start: %s: %w", name, err)
	}
	if info.IsDir() {
		return fmt.Errorf("dataplane: refusing to start: %s is a directory", name)
	}
	if _, err := os.ReadFile(path); err != nil {
		return fmt.Errorf("dataplane: refusing to start: %s: %w", name, err)
	}
	return nil
}
