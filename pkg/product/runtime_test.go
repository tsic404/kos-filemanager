package product

import (
	"strings"
	"testing"

	"github.com/tsic404/kos-sdk/packaging"
)

// TestEntryMustNameACarriedAsset checks the packer's entry rule: the frame loads
// the document at the entry path, so a path the package does not carry is
// refused before any artifact exists (KOSD-C-73).
func TestEntryMustNameACarriedAsset(t *testing.T) {
	assets := []packaging.Asset{{Path: DocumentAsset, Content: []byte("<!doctype html>")}}
	if err := checkEntryCarried("https://files.example/"+DocumentAsset, assets); err != nil {
		t.Fatalf("the carried document was refused: %v", err)
	}
	for _, entry := range []string{
		"https://files.example/index.html",
		"https://files.example/assets/missing.html",
		"https://files.example/",
	} {
		if err := checkEntryCarried(entry, assets); err == nil {
			t.Fatalf("entry %q was accepted although the package carries no such asset", entry)
		}
	}
}

// TestOriginIsTheEntryOrigin checks the derivation every address on the origin
// uses: the document, its sub-assets and the backend health probe share it
// (KOSD-C-73).
func TestOriginIsTheEntryOrigin(t *testing.T) {
	origin, err := Origin("https://FileManager.KOS.Local/assets/index.html")
	if err != nil {
		t.Fatalf("origin: %v", err)
	}
	if origin != "https://filemanager.kos.local" {
		t.Fatalf("origin = %q, want the lower-cased entry origin", origin)
	}
	if health := BackendHealth("https://filemanager.kos.local/assets/index.html"); health != "https://filemanager.kos.local"+HealthPath {
		t.Fatalf("health = %q, want the absolute health address on the entry origin", health)
	}
	for _, entry := range []string{"", "files.example", "http://files.example/assets/index.html", "https://user:secret@files.example/assets/index.html"} {
		if _, err := Origin(entry); err == nil {
			t.Fatalf("entry %q was accepted as an https origin", entry)
		}
	}
	if strings.TrimSpace(DefaultEntryOrigin) == "" {
		t.Fatal("the default entry origin is empty")
	}
}

// TestRuntimeTargetsThePackagedApplication checks the deployment document: the
// Endpoint's target application is the identity the package declares, and its
// host is the entry origin, so the entry object comes from the product rather
// than from a caller (KOSD-C-45).
func TestRuntimeTargetsThePackagedApplication(t *testing.T) {
	pkg, _, err := Build(DefaultEntryOrigin)
	if err != nil {
		t.Fatalf("build the package: %v", err)
	}
	application := pkg.Manifest.Contributions.Apps[0]
	runtime, err := RuntimeFor(application, "team")
	if err != nil {
		t.Fatalf("derive the runtime document: %v", err)
	}
	if got := runtime.Endpoint["spec"].(map[string]any)["targetApp"]; got != application.ID {
		t.Fatalf("endpoint targetApp = %v, want the packaged application %q", got, application.ID)
	}
	if got := runtime.Endpoint["spec"].(map[string]any)["host"]; got != "filemanager.kos.local" {
		t.Fatalf("endpoint host = %v, want the entry origin host", got)
	}
	if got := runtime.Application["metadata"].(map[string]any)["name"]; got != application.ID {
		t.Fatalf("application name = %v, want %q", got, application.ID)
	}
	if got := runtime.Application["metadata"].(map[string]any)["namespace"]; got != "team" {
		t.Fatalf("application namespace = %v, want team", got)
	}
	if _, err := RuntimeFor(application, ""); err == nil {
		t.Fatal("a runtime document without a namespace was accepted")
	}
}
