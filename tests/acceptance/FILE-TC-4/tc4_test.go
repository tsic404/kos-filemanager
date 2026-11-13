// Package filetc4 checks FILE-TC-4: a browse reads the volume's current
// content, every entry carries its name, type, size and modification time, and
// a path that leaves the volume root — an out-of-bounds path or a symbolic link
// pointing outside — is refused with a reason while its content never enters a
// listing (FILE-AC-3, FILE-C-3).
//
// The files are written straight into the directory this host serves for the
// volume, so the case observes what a mounted volume would present. The volume
// object itself is written by the volume reconciler's status (KOSD-C-38); this
// case only writes the status that reconciler publishes.
package filetc4

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	volumeapi "github.com/tsic404/kosd/pkg/volume"
	"github.com/tsic404/kosd/pkg/write"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-filemanager/pkg/dataplane"
	"github.com/tsic404/kos-filemanager/tests/acceptance/fixtures"
)

// modified is the modification time the case writes on one file, so the entry's
// modifiedAt is compared with an exact instant rather than the wall clock.
var modified = time.Date(2026, 4, 2, 9, 30, 0, 0, time.UTC)

// TestTC4BrowseReadsCurrentContentWithEntryFields walks one directory twice:
// the first browse shows the files it holds with their four fields, and a
// deletion, a rename and a new file made between the two reads show up on the
// second browse (FILE-C-3).
func TestTC4BrowseReadsCurrentContentWithEntryFields(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "browse-data", fixtures.MemberOne, volumeapi.PhaseBound)
	root := system.VolumeDir(t, harness.Namespace, "browse-data")

	alphaContent := "alpha-content\n"
	writeFile(t, filepath.Join(root, "alpha.txt"), alphaContent)
	setModified(t, filepath.Join(root, "alpha.txt"), modified)
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	writeFile(t, filepath.Join(root, "notes.txt"), "notes\n")

	window := system.OpenWindow(t, fixtures.MemberOne)

	status, body := system.DataPlane(t, http.MethodGet, entriesPath(harness.Namespace, "browse-data", ""),
		fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("browse = %d %v, want 200", status, body)
	}
	entries := listingEntries(t, body)
	byName := entriesByName(entries)
	if len(byName) != 3 {
		t.Fatalf("browse lists %v, want the three entries the volume holds", namesOf(entries))
	}

	alpha, found := byName["alpha.txt"]
	if !found {
		t.Fatalf("browse lists %v, want alpha.txt", namesOf(entries))
	}
	if kind, _ := alpha["kind"].(string); kind != dataplane.KindFile {
		t.Fatalf("alpha.txt kind = %v, want %q", alpha["kind"], dataplane.KindFile)
	}
	if size, _ := alpha["size"].(float64); int64(size) != int64(len(alphaContent)) {
		t.Fatalf("alpha.txt size = %v, want %d", alpha["size"], len(alphaContent))
	}
	if got := modifiedAt(t, alpha); !got.Equal(modified) {
		t.Fatalf("alpha.txt modifiedAt = %s, want %s", got, modified)
	}
	if kind, _ := byName["docs"]["kind"].(string); kind != dataplane.KindDirectory {
		t.Fatalf("docs kind = %v, want %q", byName["docs"]["kind"], dataplane.KindDirectory)
	}
	if truncated, _ := body["truncated"].(bool); truncated {
		t.Fatalf("listing of three entries is marked truncated: %v", body)
	}
	if _, ok := body["observedAt"].(string); !ok {
		t.Fatalf("listing carries no observation time (KOSD-C-13): %v", body)
	}

	// Another window deletes one file, renames another and creates a third; the
	// next browse of the same window has to see all three changes.
	if err := os.Remove(filepath.Join(root, "alpha.txt")); err != nil {
		t.Fatalf("delete alpha.txt: %v", err)
	}
	if err := os.Rename(filepath.Join(root, "notes.txt"), filepath.Join(root, "renamed.txt")); err != nil {
		t.Fatalf("rename notes.txt: %v", err)
	}
	writeFile(t, filepath.Join(root, "beta.txt"), "beta\n")

	status, body = system.DataPlane(t, http.MethodGet, entriesPath(harness.Namespace, "browse-data", ""),
		fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("second browse = %d %v, want 200", status, body)
	}
	names := namesOf(listingEntries(t, body))
	for _, want := range []string{"beta.txt", "renamed.txt"} {
		if !contains(names, want) {
			t.Fatalf("second browse lists %v, want the change to be visible as %s", names, want)
		}
	}
	for _, gone := range []string{"alpha.txt", "notes.txt"} {
		if contains(names, gone) {
			t.Fatalf("second browse lists %v, want the removed name %s gone", names, gone)
		}
	}

	// A subdirectory is browsed by its path, and the same fields come back.
	status, body = system.DataPlane(t, http.MethodGet, entriesPath(harness.Namespace, "browse-data", "docs"),
		fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("browse docs = %d %v, want 200", status, body)
	}
	if entries := listingEntries(t, body); len(entries) != 0 {
		t.Fatalf("browse docs lists %v, want the empty directory", namesOf(entries))
	}
}

// TestTC4RootBoundaryRefusesOutsideAndSymlinks checks FILE-C-3's boundary: a
// path above the volume root and a symbolic link pointing outside the volume are
// both refused with a readable reason, and neither the outside file nor its
// content ever appears in a listing.
func TestTC4RootBoundaryRefusesOutsideAndSymlinks(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "browse-data", fixtures.MemberOne, volumeapi.PhaseBound)
	root := system.VolumeDir(t, harness.Namespace, "browse-data")
	writeFile(t, filepath.Join(root, "inside.txt"), "inside-content\n")

	// The outside directory lives beside the served base, as another volume or a
	// host directory would.
	outside := t.TempDir()
	outsideContent := "leak-content\n"
	writeFile(t, filepath.Join(outside, "leak.txt"), outsideContent)
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Fatalf("link the outside directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "leak.txt"), filepath.Join(root, "leak-link.txt")); err != nil {
		t.Fatalf("link the outside file: %v", err)
	}

	window := system.OpenWindow(t, fixtures.MemberOne)

	browsable := entriesPath(harness.Namespace, "browse-data", "")
	status, body := system.DataPlane(t, http.MethodGet, browsable, fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("browse = %d %v, want 200", status, body)
	}
	// The two links are listed as the links they are: naming either one a file
	// or a directory would mean the listing followed it out of the volume, and
	// the outside file would then be a listing entry of its own.
	links := entriesByName(listingEntries(t, body))
	for _, name := range []string{"outside", "leak-link.txt"} {
		if kind, _ := links[name]["kind"].(string); kind != dataplane.KindSymlink {
			t.Fatalf("%s kind = %v, want %q", name, links[name]["kind"], dataplane.KindSymlink)
		}
	}
	if kind, found := links["leak.txt"]["kind"].(string); found && kind != dataplane.KindSymlink {
		t.Fatalf("browse lists the outside file as %q, want no out-of-volume content", kind)
	}

	// Browsing the link, or a path through it, is refused and the refusal never
	// carries the outside content.
	for _, path := range []string{"outside", "outside/leak.txt"} {
		status, body := system.DataPlane(t, http.MethodGet,
			entriesPath(harness.Namespace, "browse-data", path), fixtures.Bearer(window.Token))
		if status != http.StatusForbidden {
			t.Fatalf("browse %q = %d %v, want 403", path, status, body)
		}
		if code := fixtures.ErrorCode(t, body); code != dataplane.CodePathOutside {
			t.Fatalf("browse %q code = %q, want %q", path, code, dataplane.CodePathOutside)
		}
		if reason := fixtures.ErrorMessage(t, body); !strings.Contains(reason, "卷根") {
			t.Fatalf("browse %q reason = %q, want it to name the volume root", path, reason)
		}
		assertNoContent(t, body, outsideContent)
	}

	// A path above the root, and an absolute one, are refused before the
	// filesystem is asked for them.
	for _, path := range []string{"..", "../browse-data", "docs/../../outside", "/etc", "/etc/passwd"} {
		status, body := system.DataPlane(t, http.MethodGet,
			entriesPath(harness.Namespace, "browse-data", path), fixtures.Bearer(window.Token))
		if status != http.StatusBadRequest {
			t.Fatalf("browse %q = %d %v, want 400", path, status, body)
		}
		if code := fixtures.ErrorCode(t, body); code != dataplane.CodePathInvalid {
			t.Fatalf("browse %q code = %q, want %q", path, code, dataplane.CodePathInvalid)
		}
		if reason := fixtures.ErrorMessage(t, body); reason == "" {
			t.Fatalf("browse %q carries no reason: %v", path, body)
		}
	}

	// The refused requests left the listing as it was: the volume still lists
	// what it holds, and nothing from outside it.
	status, body = system.DataPlane(t, http.MethodGet, browsable, fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("browse after the refusals = %d %v, want 200", status, body)
	}
	names := namesOf(listingEntries(t, body))
	if !contains(names, "inside.txt") {
		t.Fatalf("browse after the refusals lists %v, want inside.txt", names)
	}
	assertNoContent(t, body, outsideContent)
}

// TestTC4LongListingIsTruncatedAndMarked checks FILE-C-3's bound: a directory
// past the limit is cut, and the answer says so instead of silently returning a
// partial list.
func TestTC4LongListingIsTruncatedAndMarked(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "browse-data", fixtures.MemberOne, volumeapi.PhaseBound)
	root := system.VolumeDir(t, harness.Namespace, "browse-data")

	total := dataplane.MaxDirectoryEntries + 3
	for index := range total {
		writeFile(t, filepath.Join(root, fmt.Sprintf("entry-%04d.txt", index)), "x")
	}

	window := system.OpenWindow(t, fixtures.MemberOne)
	status, body := system.DataPlane(t, http.MethodGet, entriesPath(harness.Namespace, "browse-data", ""),
		fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("browse = %d %v, want 200", status, body)
	}
	entries := listingEntries(t, body)
	if len(entries) != dataplane.MaxDirectoryEntries {
		t.Fatalf("truncated listing carries %d entries, want %d", len(entries), dataplane.MaxDirectoryEntries)
	}
	if truncated, _ := body["truncated"].(bool); !truncated {
		t.Fatalf("listing of %d entries is not marked truncated: %v", total, body)
	}
	if limit, _ := body["limit"].(float64); int(limit) != dataplane.MaxDirectoryEntries {
		t.Fatalf("limit = %v, want %d", body["limit"], dataplane.MaxDirectoryEntries)
	}
	if got, _ := body["total"].(float64); int(got) != total {
		t.Fatalf("total = %v, want %d", body["total"], total)
	}
	names := namesOf(entries)
	if !sort.StringsAreSorted(names) {
		t.Fatalf("truncated listing is not in name order: %v", names[:3])
	}
}

// entriesPath names one browse of one volume with the path inside it.
func entriesPath(namespace, name, inside string) string {
	route := "/files/volumes/" + namespace + "/" + name + "/entries"
	if inside == "" {
		return route
	}
	return route + "?path=" + url.QueryEscape(inside)
}

// listingEntries reads the entry list of one browse answer.
func listingEntries(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["entries"].([]any)
	if !ok {
		t.Fatalf("answer carries no entry list: %v", body)
	}
	entries := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("entry is not an object: %v", item)
		}
		entries = append(entries, entry)
	}
	return entries
}

// entriesByName indexes one listing by entry name.
func entriesByName(entries []map[string]any) map[string]map[string]any {
	byName := make(map[string]map[string]any, len(entries))
	for _, entry := range entries {
		name, _ := entry["name"].(string)
		byName[name] = entry
	}
	return byName
}

// namesOf reads the entry names in answer order.
func namesOf(entries []map[string]any) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name, _ := entry["name"].(string)
		names = append(names, name)
	}
	return names
}

// modifiedAt reads one entry's modification time.
func modifiedAt(t *testing.T, entry map[string]any) time.Time {
	t.Helper()
	raw, ok := entry["modifiedAt"].(string)
	if !ok {
		t.Fatalf("entry carries no modification time: %v", entry)
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("parse modifiedAt %q: %v", raw, err)
	}
	return parsed
}

// contains reports whether one name is in the list.
func contains(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// assertNoContent checks that one answer carries none of the given content.
func assertNoContent(t *testing.T, body map[string]any, content string) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode the answer: %v", err)
	}
	if strings.Contains(string(encoded), strings.TrimSpace(content)) {
		t.Fatalf("answer carries out-of-volume content: %s", encoded)
	}
}

// writeFile writes one file into the served directory.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// setModified pins one file's modification time.
func setModified(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatalf("set the modification time of %s: %v", path, err)
	}
}
