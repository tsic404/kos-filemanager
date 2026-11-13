// Package filetc1 checks FILE-TC-1: a window lists exactly the volumes its
// account owns, an exposed-volume preference naming another account's volume is
// kept on the settings surface but never widens the list, and a second account's
// window lists only its own volumes (FILE-AC-1, FILE-C-1, KOSD-C-60).
package filetc1

import (
	"net/http"
	"testing"

	"github.com/tsic404/kosd/pkg/write"

	"github.com/tsic404/kos-filemanager/pkg/product"
	"github.com/tsic404/kos-filemanager/tests/acceptance/fixtures"
)

func TestTC1AuthorizedScopeAndPreference(t *testing.T) {
	system := fixtures.Start(t)
	aliceSession := system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.Account(t, fixtures.MemberTwo, fixtures.MemberTwoPass, write.RoleMember)
	system.CreateVolume(t, "alice-data", fixtures.MemberOne, "Bound")
	system.CreateVolume(t, "bob-data", fixtures.MemberTwo, "Bound")

	// The first session sees one volume: the one its account owns.
	aliceWindow := system.OpenWindow(t, fixtures.MemberOne)
	status, body := system.DataPlane(t, http.MethodGet, "/files/window", fixtures.Bearer(aliceWindow.Token))
	if status != http.StatusOK {
		t.Fatalf("alice window = %d %v, want 200", status, body)
	}
	requireVolumeNames(t, body, "alice-data")

	// The settings surface keeps a reference to another account's volume; the
	// list still does not contain it, and the preference is still readable. The
	// view and initial path are read back into the next window.
	system.SetPreference(t, aliceSession, product.PreferenceExposedVolumes, `["default/bob-data"]`)
	system.SetPreference(t, aliceSession, product.PreferenceView, product.ViewGrid)
	system.SetPreference(t, aliceSession, product.PreferenceInitialPath, "/documents")
	system.CloseWindow(t, fixtures.MemberOne, aliceWindow)
	aliceWindow = system.OpenWindow(t, fixtures.MemberOne)
	status, body = system.DataPlane(t, http.MethodGet, "/files/window", fixtures.Bearer(aliceWindow.Token))
	if status != http.StatusOK {
		t.Fatalf("reopened alice window = %d %v, want 200", status, body)
	}
	requireVolumeNames(t, body, "alice-data")
	if view, _ := body["view"].(string); view != product.ViewGrid {
		t.Fatalf("view = %q, want the stored %q", view, product.ViewGrid)
	}
	if path, _ := body["initialPath"].(string); path != "/documents" {
		t.Fatalf("initialPath = %q, want the stored /documents", path)
	}
	if stored := system.Preferences(t, aliceSession)[product.PreferenceExposedVolumes]; stored != `["default/bob-data"]` {
		t.Fatalf("exposed volumes = %q, want the stored reference kept on the settings surface", stored)
	}

	// The second session's list contains only the volume its own account owns.
	bobWindow := system.OpenWindow(t, fixtures.MemberTwo)
	status, body = system.DataPlane(t, http.MethodGet, "/files/window", fixtures.Bearer(bobWindow.Token))
	if status != http.StatusOK {
		t.Fatalf("bob window = %d %v, want 200", status, body)
	}
	requireVolumeNames(t, body, "bob-data")
}

// requireVolumeNames asserts the window's volume list names exactly these
// volumes, in this order.
func requireVolumeNames(t *testing.T, body map[string]any, want ...string) {
	t.Helper()
	raw, ok := body["volumes"].([]any)
	if !ok {
		t.Fatalf("window carries no volume list: %v", body)
	}
	if len(raw) != len(want) {
		t.Fatalf("window lists %d volumes %v, want %v", len(raw), raw, want)
	}
	for index, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("volume entry is not an object: %v", item)
		}
		if name, _ := entry["name"].(string); name != want[index] {
			t.Fatalf("volume %d = %q, want %q", index, name, want[index])
		}
	}
}
