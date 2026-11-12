package product

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/tsic404/kos-sdk/manifest"

	"github.com/tsic404/kosd/api"
	"github.com/tsic404/kosd/api/v1beta1"
)

// Runtime is the deployment document this product owns beside its package: the
// Application instance the window application belongs to and the Endpoint that
// exposes its data plane. The Endpoint's target application is that instance, so
// the entry names this product rather than whoever deploys it (KOSD-C-45,
// KOSD-C-73). The workload that serves the backend is a deployment concern and
// carries no product identity of its own.
type Runtime struct {
	// Application is the application instance the window host addresses.
	Application map[string]any `json:"application"`
	// Endpoint is the data-plane entry whose target application is that instance.
	Endpoint map[string]any `json:"endpoint"`
}

// RuntimeFor derives the deployment document from one packaged application
// declaration. It reads the identity and the entry address from the manifest the
// package carries, so a manifest change cannot leave the entry behind
// (KOSD-C-45).
func RuntimeFor(application manifest.AppManifest, namespace string) (Runtime, error) {
	if application.ID == "" {
		return Runtime{}, errors.New("product: the application declaration names no identity")
	}
	if namespace == "" {
		return Runtime{}, errors.New("product: the runtime document needs a target namespace")
	}
	origin, err := Origin(application.Entry)
	if err != nil {
		return Runtime{}, err
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return Runtime{}, fmt.Errorf("product: entry %q has no host", application.Entry)
	}
	apiVersion := api.Group + "/" + v1beta1.GroupVersion.Version
	metadata := map[string]any{"name": application.ID, "namespace": namespace}
	return Runtime{
		Application: map[string]any{
			"apiVersion": apiVersion,
			"kind":       "Application",
			"metadata":   metadata,
			"spec":       map[string]any{},
		},
		Endpoint: map[string]any{
			"apiVersion": apiVersion,
			"kind":       "Endpoint",
			"metadata":   map[string]any{"name": application.ID, "namespace": namespace},
			"spec": map[string]any{
				"kind":      string(v1beta1.EndpointKindHTTPHost),
				"targetApp": application.ID,
				"host":      parsed.Host,
			},
		},
	}, nil
}
