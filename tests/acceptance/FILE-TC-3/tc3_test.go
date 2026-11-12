// Package filetc3 checks FILE-TC-3: the three volume phases are distinguishable
// in the window, a Pending and a Lost volume are refused with a reason that
// names the state, and a volume that recovers is usable on the next request
// without reopening the window (FILE-AC-2, FILE-C-2, KOSD-C-38).
//
// The phase transitions themselves belong to the volume reconciler (KOSD-C-38);
// the case writes the status that reconciler publishes and asserts only that
// this product reflects it.
package filetc3

import (
	"net/http"
	"strings"
	"testing"

	volumeapi "github.com/tsic404/kosd/pkg/volume"
	"github.com/tsic404/kosd/pkg/write"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-filemanager/pkg/dataplane"
	"github.com/tsic404/kos-filemanager/tests/acceptance/fixtures"
)

func TestTC3VolumeStatusAndRecovery(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "bound-data", fixtures.MemberOne, volumeapi.PhaseBound)
	system.CreateVolume(t, "pending-data", fixtures.MemberOne, volumeapi.PhasePending)
	system.CreateVolume(t, "lost-data", fixtures.MemberOne, volumeapi.PhaseLost)
	system.SetVolumeStatus(t, "lost-data", volumeapi.PhaseLost, "node-loss")

	window := system.OpenWindow(t, fixtures.MemberOne)
	status, body := system.DataPlane(t, http.MethodGet, "/files/window", fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("window = %d %v, want 200", status, body)
	}
	byName := map[string]map[string]any{}
	for _, entry := range fixtures.VolumeEntries(t, body) {
		byName[entry["name"].(string)] = entry
	}
	if len(byName) != 3 {
		t.Fatalf("window lists %d volumes, want all three phases visible: %v", len(byName), body)
	}
	if phase, _ := byName["bound-data"]["phase"].(string); phase != volumeapi.PhaseBound {
		t.Fatalf("bound phase = %q, want %q", phase, volumeapi.PhaseBound)
	}
	if phase, _ := byName["pending-data"]["phase"].(string); phase != volumeapi.PhasePending {
		t.Fatalf("pending phase = %q, want %q", phase, volumeapi.PhasePending)
	}
	if phase, _ := byName["lost-data"]["phase"].(string); phase != volumeapi.PhaseLost {
		t.Fatalf("lost phase = %q, want %q", phase, volumeapi.PhaseLost)
	}
	if canBrowse, _ := byName["bound-data"]["canBrowse"].(bool); !canBrowse {
		t.Fatalf("bound volume = %v, want it browsable", byName["bound-data"])
	}

	// Both unavailable states are refused with a reason that names the state.
	for name, want := range map[string]string{"pending-data": "绑定", "lost-data": "丢失"} {
		status, body := system.DataPlane(t, http.MethodGet, "/files/volumes/"+harness.Namespace+"/"+name,
			fixtures.Bearer(window.Token))
		if status != http.StatusConflict {
			t.Fatalf("%s browse = %d %v, want 409", name, status, body)
		}
		if code := fixtures.ErrorCode(t, body); code != dataplane.CodeVolumeUnavailable {
			t.Fatalf("%s refusal code = %q, want %q", name, code, dataplane.CodeVolumeUnavailable)
		}
		if reason := fixtures.ErrorMessage(t, body); !strings.Contains(reason, want) {
			t.Fatalf("%s refusal reason = %q, want it to name %q", name, reason, want)
		}
	}

	// The volume recovers; the same window and token see it on the next read.
	system.SetVolumeStatus(t, "pending-data", volumeapi.PhaseBound, "")
	status, body = system.DataPlane(t, http.MethodGet, "/files/window", fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("window after recovery = %d %v, want 200", status, body)
	}
	for _, entry := range fixtures.VolumeEntries(t, body) {
		if entry["name"] == "pending-data" {
			if canBrowse, _ := entry["canBrowse"].(bool); !canBrowse {
				t.Fatalf("recovered volume = %v, want it browsable without reopening", entry)
			}
		}
	}
	status, body = system.DataPlane(t, http.MethodGet, "/files/volumes/"+harness.Namespace+"/pending-data",
		fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("browse after recovery = %d %v, want 200", status, body)
	}
}
