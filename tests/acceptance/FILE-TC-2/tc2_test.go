// Package filetc2 checks FILE-TC-2: the data plane answers only with the
// window's application token, a closed window's token and a console session
// cookie are refused, and the data-plane entry object targets this application
// (FILE-AC-1, FILE-C-1, KOSD-C-45, KOSD-C-75).
package filetc2

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
	"github.com/tsic404/kosd/pkg/write"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-filemanager/pkg/dataplane"
	"github.com/tsic404/kos-filemanager/pkg/product"
	"github.com/tsic404/kos-filemanager/tests/acceptance/fixtures"
)

func TestTC2ApplicationTokenAndEntryOwnership(t *testing.T) {
	system := fixtures.Start(t)
	aliceSession := system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "alice-data", fixtures.MemberOne, "Bound")

	// The window's token works while the window is open.
	window := system.OpenWindow(t, fixtures.MemberOne)
	status, body := system.DataPlane(t, http.MethodGet, "/files/window", fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("open window = %d %v, want 200", status, body)
	}

	// Closing the window revokes the token: the same value is refused and the
	// answer asks for the window to be reopened (KOSD-C-75).
	system.CloseWindow(t, fixtures.MemberOne, window)
	status, body = system.DataPlane(t, http.MethodGet, "/files/window", fixtures.Bearer(window.Token))
	if status != http.StatusUnauthorized {
		t.Fatalf("closed window token = %d %v, want 401", status, body)
	}
	if code := failureCode(t, body); code != dataplane.CodeTokenInvalid {
		t.Fatalf("refusal code = %q, want %q", code, dataplane.CodeTokenInvalid)
	}

	// The console session cookie, sent alone, is not an identity of the data
	// plane even though it is a live control-plane session.
	status, body = system.DataPlane(t, http.MethodGet, "/files/window", fixtures.ConsoleCookie(aliceSession))
	if status != http.StatusUnauthorized {
		t.Fatalf("console session cookie = %d %v, want 401", status, body)
	}
	if code := failureCode(t, body); code != dataplane.CodeUnauthenticated {
		t.Fatalf("refusal code = %q, want %q", code, dataplane.CodeUnauthenticated)
	}

	// Reopening the window delivers a fresh token that works again.
	window = system.OpenWindow(t, fixtures.MemberOne)
	if status, body = system.DataPlane(t, http.MethodGet, "/files/window", fixtures.Bearer(window.Token)); status != http.StatusOK {
		t.Fatalf("reopened window = %d %v, want 200", status, body)
	}

	// The entry object comes from the product's deployment document: the entry
	// targets the application the packaged manifest declares, not a value this
	// case invented (KOSD-C-45).
	pkg, _, err := product.Build(product.DefaultEntryOrigin)
	if err != nil {
		t.Fatalf("build the package: %v", err)
	}
	declared := pkg.Manifest.Contributions.Apps[0]
	runtime, err := product.RuntimeFor(declared, harness.Namespace)
	if err != nil {
		t.Fatalf("derive the runtime document: %v", err)
	}
	system.ApplyRuntime(t, runtime)
	endpoints := system.EntryEndpoints(t, system.OwnerToken, declared.ID)
	if len(endpoints) != 1 {
		t.Fatalf("entries targeting %s = %d, want one", declared.ID, len(endpoints))
	}
	spec := endpoints[0].Object["spec"].(map[string]any)
	if target := spec["targetApp"]; target != declared.ID {
		t.Fatalf("entry targetApp = %v, want the packaged application %q", target, declared.ID)
	}
	if host := spec["host"]; host != "filemanager.kos.local" {
		t.Fatalf("entry host = %v, want the entry origin host", host)
	}

	// The window declaration the console consumes: the packaged manifest opens
	// a resizable single-instance window at the declared default and minimum
	// size, in the declared default state (KOSD-C-73, DASH-C-74).
	application := system.Application(t)
	if application.Window.Width != product.WindowWidth || application.Window.Height != product.WindowHeight {
		t.Fatalf("window default size = %dx%d, want %dx%d",
			application.Window.Width, application.Window.Height, product.WindowWidth, product.WindowHeight)
	}
	if application.Window.MinWidth != product.WindowMinWidth || application.Window.MinHeight != product.WindowMinHeight {
		t.Fatalf("window minimum size = %dx%d, want %dx%d",
			application.Window.MinWidth, application.Window.MinHeight, product.WindowMinWidth, product.WindowMinHeight)
	}
	if application.Window.MinWidth > application.Window.Width || application.Window.MinHeight > application.Window.Height {
		t.Fatalf("window minimum %dx%d exceeds its default %dx%d",
			application.Window.MinWidth, application.Window.MinHeight,
			application.Window.Width, application.Window.Height)
	}
	if !application.Window.Resizable || application.Window.Mode != apphost.WindowModeSingle ||
		application.Window.DefaultState != apphost.DefaultStateNormal {
		t.Fatalf("window = %+v, want a resizable single window in the normal state", application.Window)
	}

	// The icon the launcher shows is carried by the package, not fetched from
	// an unaddressed source (KOSD-C-73).
	if application.Icon != product.IconAsset {
		t.Fatalf("icon = %q, want the carried asset %q", application.Icon, product.IconAsset)
	}
	assets, err := product.Assets()
	if err != nil {
		t.Fatalf("read the package assets: %v", err)
	}
	iconCarried := false
	for _, asset := range assets {
		if asset.Path == product.IconAsset && len(asset.Content) > 0 {
			iconCarried = true
		}
	}
	if !iconCarried {
		t.Fatalf("package assets carry no %s, want the declared icon", product.IconAsset)
	}

	// The desktop derives its entry from these surfaces (DASH-C-30).
	surfaces := make(map[string]bool, len(application.Surfaces))
	for _, surface := range application.Surfaces {
		surfaces[surface] = true
	}
	if !surfaces[apphost.SurfaceLauncher] || !surfaces[apphost.SurfaceDock] {
		t.Fatalf("surfaces = %v, want the launcher and the dock", application.Surfaces)
	}

	// The entry is the window document the package carries, on an origin of its
	// own, and the backend health address is absolute on that origin (KOSD-C-73).
	entry, err := url.Parse(application.Entry)
	if err != nil || entry.Scheme != "https" || entry.Host == "" ||
		entry.Path != "/"+product.DocumentAsset || entry.User != nil || entry.RawQuery != "" {
		t.Fatalf("entry = %q, want the carried document on its own absolute https origin", application.Entry)
	}
	if application.Backend == nil {
		t.Fatal("the application declares no backend; the data plane would be unreachable")
	}
	if application.Backend.Type != product.BackendType {
		t.Fatalf("backend type = %q, want %q", application.Backend.Type, product.BackendType)
	}
	health, err := url.Parse(application.Backend.Health)
	if err != nil || !health.IsAbs() || health.Scheme != "https" || health.Host != entry.Host {
		t.Fatalf("backend health = %q, want an absolute address on %s", application.Backend.Health, entry.Host)
	}

	// Single mode answers the instance the same browser session already holds
	// instead of opening another window (DASH-C-74); the host reports the
	// manifest's defaults for the instance it opened.
	system.CloseWindow(t, fixtures.MemberOne, window)
	first, opened := system.OpenInstance(t, fixtures.MemberOne)
	if !opened {
		t.Fatal("the first open reused a window, want a new single-mode instance")
	}
	if first.DefaultWidth != product.WindowWidth || first.DefaultHeight != product.WindowHeight ||
		first.DefaultState != product.WindowDefaultState || first.Mode != product.WindowModeSingle {
		t.Fatalf("opened window = %+v, want the declared defaults", first)
	}
	second, openedAgain := system.OpenInstance(t, fixtures.MemberOne)
	if openedAgain {
		t.Fatal("a single-mode application opened a second instance")
	}
	if second.ID != first.ID {
		t.Fatalf("focused window = %q, want the already open %q", second.ID, first.ID)
	}
}

// failureCode reads the code of one refusal.
func failureCode(t *testing.T, body map[string]any) string {
	t.Helper()
	failure, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("answer carries no error envelope: %v", body)
	}
	code, _ := failure["code"].(string)
	return code
}
