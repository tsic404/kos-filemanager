// Package kosdtc155 checks KOSD-TC-155 for this product: the artifact the SDK
// packs installs through the one extension path as a system component, its
// window application appears in the control plane's application projection and
// in the window host, and uninstalling the component withdraws exactly this
// product's contributions — window, preference schema and preference data —
// while a peer product keeps serving and the kernel kind set stays unchanged
// (KOSD-C-17, KOSD-C-18, KOSD-C-73, KOSD-AC-72).
package kosdtc155

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/tsic404/kosd/api"
	"github.com/tsic404/kosd/pkg/extension"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-filemanager/pkg/product"
	"github.com/tsic404/kos-filemanager/tests/acceptance/fixtures"
)

const peerDomain = "reporting.example"

func TestTC155InstallsTheWindowApplication(t *testing.T) {
	system := fixtures.Start(t)
	kindsBefore := append([]string(nil), api.Kinds()...)
	pkg, _ := fixtures.ProductArtifact(t)

	// The package installed as a system component is Ready, its declaration
	// loaded, and the window application it declares is the one the package
	// carries (KOSD-C-17, KOSD-C-18).
	component, found := system.Installation.Component(product.Identifier)
	if !found {
		t.Fatalf("the install pass holds no component %s", product.Identifier)
	}
	if component.Phase != extension.PhaseReady {
		t.Fatalf("component phase = %q, want %q: %+v", component.Phase, extension.PhaseReady, component)
	}
	if len(component.Problems) != 0 {
		t.Fatalf("component problems = %+v, want none", component.Problems)
	}
	if component.Digest != pkg.Digest.String() {
		t.Fatalf("component digest = %q, want the verified package digest %q", component.Digest, pkg.Digest.String())
	}
	if len(component.Contributions.Apps) != 1 {
		t.Fatalf("component apps = %+v, want the declared window application", component.Contributions.Apps)
	}

	installed := system.Installation.Registry().Apps()
	if len(installed) != 1 {
		t.Fatalf("installed applications = %+v, want exactly this product's window", installed)
	}
	application := installed[0]
	if application.Extension != product.Identifier || application.Component != product.Identifier {
		t.Fatalf("application belongs to %s/%s, want %s", application.Extension, application.Component, product.Identifier)
	}
	if application.Phase != extension.PhaseReady || application.Digest != pkg.Digest.String() {
		t.Fatalf("application phase/digest = %s/%q, want %q and the package digest",
			application.Phase, application.Digest, extension.PhaseReady)
	}
	if application.Manifest.ID != product.AppID || application.Manifest.Version != product.Version {
		t.Fatalf("application manifest names %s@%s, want %s@%s",
			application.Manifest.ID, application.Manifest.Version, product.AppID, product.Version)
	}
	if application.Manifest.Entry != "https://filemanager.kos.local/"+product.DocumentAsset {
		t.Fatalf("application entry = %q, want the carried document address", application.Manifest.Entry)
	}
	if application.Manifest.Backend == nil ||
		application.Manifest.Backend.Health != "https://filemanager.kos.local"+product.HealthPath {
		t.Fatalf("application backend = %+v, want the data plane's health address", application.Manifest.Backend)
	}

	// The window host loads the same contribution: the installed manifest
	// satisfies KOSD-C-73, so the entry is usable rather than refused.
	loaded := system.Application(t)
	if loaded.ID != product.AppID || loaded.Digest != pkg.Digest.String() {
		t.Fatalf("window host loaded %s with digest %q, want %s and the package digest",
			loaded.ID, loaded.Digest, product.AppID)
	}

	// The control plane's projection is what the desktop reads: one application,
	// with the declaring extension, the component, the phase and the digest.
	applications := system.Applications(t, system.OwnerToken)
	if len(applications) != 1 {
		t.Fatalf("control-plane applications = %+v, want the installed window", applications)
	}
	projected := applications[0]
	if projected["extension"] != product.Identifier || projected["component"] != product.Identifier ||
		projected["phase"] != extension.PhaseReady || projected["digest"] != pkg.Digest.String() {
		t.Fatalf("projected application = %+v, want this product's component, phase and digest", projected)
	}
	manifest, _ := projected["manifest"].(map[string]any)
	if manifest["id"] != product.AppID {
		t.Fatalf("projected manifest = %+v, want application %s", manifest, product.AppID)
	}
	window, _ := manifest["window"].(map[string]any)
	if window["width"] != float64(product.WindowWidth) || window["mode"] != product.WindowModeSingle {
		t.Fatalf("projected window = %+v, want the declared defaults", window)
	}

	// The declared preference structure and the volume read scope loaded, and the
	// product declares nothing it does not contribute.
	keys, declared := system.Installation.Registry().PreferenceKeys(product.PreferenceDomain)
	if !declared || !reflect.DeepEqual(keys, product.PreferenceKeys) {
		t.Fatalf("preference keys = %v (declared %v), want %v", keys, declared, product.PreferenceKeys)
	}
	if len(component.Contributions.Pages) != 0 || len(component.Contributions.Controllers) != 0 ||
		len(component.Contributions.Settings) != 0 {
		t.Fatalf("the product declares contributions it should not: %+v", component.Contributions)
	}

	// Both this product and a peer store preferences through the same domain, and
	// installing the peer leaves this product's window serving.
	system.SetPreference(t, system.OwnerToken, product.PreferenceView, product.ViewGrid)
	peer := system.Cluster.InstallPackageWith(t, fixtures.ThirdPartyComponent,
		fixtures.ThirdPartyDeclaration(), system.Artifacts, system.Accounts.Service)
	system.Registry.Publish(peer.Registry())
	if keys, declared := peer.Registry().PreferenceKeys(product.PreferenceDomain); !declared {
		t.Fatalf("the product's preference domain disappeared when the peer installed: %v", keys)
	}
	if keys, declared := peer.Registry().PreferenceKeys(peerDomain); !declared || len(keys) != 1 {
		t.Fatalf("peer preference keys = %v (declared %v), want one key", keys, declared)
	}
	if apps := peer.Registry().AppsOf(product.Identifier); len(apps) != 1 {
		t.Fatalf("applications of %s after the peer installed = %+v, want this product's window", product.Identifier, apps)
	}
	system.SetPreferenceIn(t, system.OwnerToken, peerDomain, "range", "7d")
	if applications := system.Applications(t, system.OwnerToken); len(applications) != 1 {
		t.Fatalf("control-plane applications after the peer installed = %+v, want still this product's window", applications)
	}

	// Uninstalling this product withdraws its window and preference schema and
	// deletes its preference data, while the peer's domain and values stay.
	system.Cluster.DeleteComponent(t, product.Identifier)
	after := system.Cluster.ExtensionStateWith(t, system.Artifacts, system.Accounts.Service)
	system.Registry.Publish(after.Registry())
	if apps := after.Registry().Apps(); len(apps) != 0 {
		t.Fatalf("applications after the uninstall = %+v, want none", apps)
	}
	if applications := system.Applications(t, system.OwnerToken); len(applications) != 0 {
		t.Fatalf("control-plane applications after the uninstall = %+v, want none", applications)
	}
	if _, declared := after.Registry().PreferenceKeys(product.PreferenceDomain); declared {
		t.Fatal("the product's preference domain survived its uninstall")
	}
	values, err := system.Accounts.Store.PreferencesOf(context.Background(), fixtures.OwnerName, product.PreferenceDomain)
	if err != nil {
		t.Fatalf("read the withdrawn preference domain: %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("withdrawn preference values = %v, want the declared delete to have removed them", values)
	}
	if keys, declared := after.Registry().PreferenceKeys(peerDomain); !declared || len(keys) != 1 {
		t.Fatalf("peer preference domain after the uninstall = %v (declared %v), want it untouched", keys, declared)
	}
	kept, err := system.Accounts.Store.PreferencesOf(context.Background(), fixtures.OwnerName, peerDomain)
	if err != nil {
		t.Fatalf("read the peer preference domain: %v", err)
	}
	if kept["range"] != "7d" {
		t.Fatalf("peer preference value = %v, want the stored 7d", kept)
	}
	// The product's preference writes are refused once its schema is gone.
	status, _ := system.Accounts.HTTP.Call(t, http.MethodPut,
		"/api/v1/preferences/"+product.PreferenceDomain+"/"+product.PreferenceView,
		map[string]any{"value": product.ViewList}, fixtures.Bearer(system.OwnerToken))
	if status == http.StatusNoContent {
		t.Fatal("a withdrawn preference domain accepted a write")
	}

	// The kernel's object model does not move with a product's install or
	// uninstall (KOSD-C-17).
	if kindsAfter := api.Kinds(); !reflect.DeepEqual(kindsAfter, kindsBefore) {
		t.Fatalf("kernel kinds before %v, after %v", kindsBefore, kindsAfter)
	}
}

// TestTC155DigestMismatchIsRefused checks the install path's digest judgement on
// this product's artifact: naming a digest the artifact does not carry leaves
// the component unavailable and installs no window (KOSD-C-17, KOSD-C-73).
func TestTC155DigestMismatchIsRefused(t *testing.T) {
	cluster := harness.Start(t)
	shell := cluster.StartShell(t)
	owner := shell.SignInOwner(t, "owner", "owner-password")
	_, archive := fixtures.ProductArtifact(t)

	// A digest in the right wire form that the artifact does not hash to.
	mismatched := "sha256:" + strings.Repeat("0", 64)
	artifacts := extension.MemoryArtifacts{}
	installation := fixtures.InstallArtifact(t, cluster, artifacts, product.Identifier, mismatched, archive, nil)
	shell.Use(installation)

	result, found := installation.Component(product.Identifier)
	if !found {
		t.Fatalf("the install pass holds no component %s", product.Identifier)
	}
	if result.Phase != extension.PhaseUnavailable || result.Reason != extension.ReasonDigestMismatch {
		t.Fatalf("component = %s/%s, want unavailable on a digest mismatch", result.Phase, result.Reason)
	}
	if apps := installation.Registry().Apps(); len(apps) != 0 {
		t.Fatalf("applications of a refused package = %+v, want none", apps)
	}

	status, body := shell.Call(t, http.MethodGet, "/api/v1/apps", nil, owner)
	if status != http.StatusOK {
		t.Fatalf("read the application projection = %d %v", status, body)
	}
	if items := harness.Items(t, body); len(items) != 0 {
		t.Fatalf("control-plane applications = %+v, want none for a refused package", items)
	}
}
