// Package filetc8 checks FILE-TC-8: after the global Owner transfers a volume,
// the former owner's window no longer lists it and the new owner's window lists
// and browses it (FILE-AC-1, FILE-C-1, KOSD-C-60).
package filetc8

import (
	"net/http"
	"testing"

	"github.com/tsic404/kosd/pkg/write"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-filemanager/tests/acceptance/fixtures"
)

func TestTC8ListScopeAfterOwnershipTransfer(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.Account(t, fixtures.MemberTwo, fixtures.MemberTwoPass, write.RoleMember)
	system.CreateVolume(t, "alice-data", fixtures.MemberOne, "Bound")
	system.CreateVolume(t, "bob-data", fixtures.MemberTwo, "Bound")

	aliceWindow := system.OpenWindow(t, fixtures.MemberOne)
	bobWindow := system.OpenWindow(t, fixtures.MemberTwo)
	requireNames(t, system, aliceWindow.Token, "alice-data")
	requireNames(t, system, bobWindow.Token, "bob-data")

	// The global Owner moves alice's volume to bob.
	system.TransferVolume(t, system.OwnerToken, "alice-data", fixtures.MemberTwo)

	// Both accounts reopen their windows: the transfer is visible to each.
	system.CloseWindow(t, fixtures.MemberOne, aliceWindow)
	system.CloseWindow(t, fixtures.MemberTwo, bobWindow)
	aliceWindow = system.OpenWindow(t, fixtures.MemberOne)
	bobWindow = system.OpenWindow(t, fixtures.MemberTwo)
	requireNames(t, system, aliceWindow.Token)
	requireNames(t, system, bobWindow.Token, "alice-data", "bob-data")

	// The new owner can browse the transferred volume's content entry.
	status, body := system.DataPlane(t, http.MethodGet,
		"/files/volumes/"+harness.Namespace+"/alice-data", fixtures.Bearer(bobWindow.Token))
	if status != http.StatusOK {
		t.Fatalf("new owner browse = %d %v, want 200", status, body)
	}
}

// requireNames asserts one window's volume list names exactly these volumes.
func requireNames(t *testing.T, system *fixtures.Case, token string, want ...string) {
	t.Helper()
	status, body := system.DataPlane(t, http.MethodGet, "/files/window", fixtures.Bearer(token))
	if status != http.StatusOK {
		t.Fatalf("window = %d %v, want 200", status, body)
	}
	names := fixtures.VolumeNames(t, body)
	if len(names) != len(want) {
		t.Fatalf("window lists %v, want %v", names, want)
	}
	for index, name := range names {
		if name != want[index] {
			t.Fatalf("window lists %v, want %v", names, want)
		}
	}
}
