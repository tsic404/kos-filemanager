// Package filetc7 checks FILE-TC-7: a rename onto a name the directory already
// holds is refused with a readable reason instead of overwriting it, a copy and
// a move take effect, a delete of a non-empty directory states what it would
// remove before it happens and only removes it once confirmed, the volume root
// is not deletable, and a copy into a read-only directory fails leaving the
// source in place and no half result behind — including a directory copy whose
// staged tree already holds read-only subtrees (FILE-AC-5, FILE-C-5).
//
// The files are written straight into the directories this host serves for the
// volumes, so the case observes what a mounted volume would present.
package filetc7

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	volumeapi "github.com/tsic404/kosd/pkg/volume"
	"github.com/tsic404/kosd/pkg/write"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-filemanager/pkg/dataplane"
	"github.com/tsic404/kos-filemanager/tests/acceptance/fixtures"
)

// TestTC7RenameRefusesAnExistingName renames one file to a name the directory
// does not hold, then asks to rename it onto a name it does hold. The first
// rename takes effect; the second is refused with a reason that names the
// conflict, and the file it would have replaced keeps its own content: no
// silent overwrite (FILE-C-5).
func TestTC7RenameRefusesAnExistingName(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "rename-data", fixtures.MemberOne, volumeapi.PhaseBound)
	root := system.VolumeDir(t, harness.Namespace, "rename-data")
	writeFile(t, filepath.Join(root, "draft.txt"), "draft body\n")
	writeFile(t, filepath.Join(root, "final.txt"), "final body\n")

	window := system.OpenWindow(t, fixtures.MemberOne)

	status, body := system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "rename-data", "rename"),
		map[string]any{"path": "draft.txt", "name": "notes.txt"}, fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("rename draft.txt to notes.txt = %d %v, want 200", status, body)
	}
	assertContent(t, filepath.Join(root, "notes.txt"), "draft body\n")
	if _, err := os.Lstat(filepath.Join(root, "draft.txt")); !os.IsNotExist(err) {
		t.Fatalf("draft.txt still exists after the rename: %v", err)
	}

	status, body = system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "rename-data", "rename"),
		map[string]any{"path": "notes.txt", "name": "final.txt"}, fixtures.Bearer(window.Token))
	if status != http.StatusConflict {
		t.Fatalf("rename onto the existing final.txt = %d %v, want 409", status, body)
	}
	if code := fixtures.ErrorCode(t, body); code != dataplane.CodeTargetExists {
		t.Fatalf("rename conflict code = %q, want %q", code, dataplane.CodeTargetExists)
	}
	reason := fixtures.ErrorMessage(t, body)
	if !strings.Contains(reason, "final.txt") || !strings.Contains(reason, "已存在") {
		t.Fatalf("rename conflict reason = %q, want it to name final.txt and the conflict", reason)
	}
	// Neither side moved: the source still has its content, and the file the
	// rename would have replaced was not overwritten.
	assertContent(t, filepath.Join(root, "notes.txt"), "draft body\n")
	assertContent(t, filepath.Join(root, "final.txt"), "final body\n")
}

// TestTC7CopyAndMoveBetweenOwnVolumes copies one file into the account's other
// volume and then moves another file there: the copy leaves the source in
// place, the move does not, and each result carries the source's content. A
// copy into a volume another account owns is refused, so both ends of a change
// stay inside the authorized volumes (FILE-C-5, FILE-C-1).
func TestTC7CopyAndMoveBetweenOwnVolumes(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.Account(t, fixtures.MemberTwo, fixtures.MemberTwoPass, write.RoleMember)
	system.CreateVolume(t, "working", fixtures.MemberOne, volumeapi.PhaseBound)
	system.CreateVolume(t, "archive", fixtures.MemberOne, volumeapi.PhaseBound)
	system.CreateVolume(t, "bob-data", fixtures.MemberTwo, volumeapi.PhaseBound)
	working := system.VolumeDir(t, harness.Namespace, "working")
	archive := system.VolumeDir(t, harness.Namespace, "archive")
	bob := system.VolumeDir(t, harness.Namespace, "bob-data")
	writeFile(t, filepath.Join(working, "report.txt"), "report body\n")
	writeFile(t, filepath.Join(working, "minutes.txt"), "minutes body\n")

	window := system.OpenWindow(t, fixtures.MemberOne)

	status, body := system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "working", "copy"),
		map[string]any{"path": "report.txt", "target": map[string]any{
			"namespace": harness.Namespace, "name": "archive", "path": "report.txt",
		}}, fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("copy report.txt into archive = %d %v, want 200", status, body)
	}
	assertContent(t, filepath.Join(working, "report.txt"), "report body\n")
	assertContent(t, filepath.Join(archive, "report.txt"), "report body\n")

	status, body = system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "working", "move"),
		map[string]any{"path": "minutes.txt", "target": map[string]any{
			"namespace": harness.Namespace, "name": "archive", "path": "minutes.txt",
		}}, fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("move minutes.txt into archive = %d %v, want 200", status, body)
	}
	assertContent(t, filepath.Join(archive, "minutes.txt"), "minutes body\n")
	if _, err := os.Lstat(filepath.Join(working, "minutes.txt")); !os.IsNotExist(err) {
		t.Fatalf("minutes.txt still exists in the source volume after the move: %v", err)
	}

	// The target must be an authorized volume just like the source.
	status, body = system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "working", "copy"),
		map[string]any{"path": "report.txt", "target": map[string]any{
			"namespace": harness.Namespace, "name": "bob-data", "path": "report.txt",
		}}, fixtures.Bearer(window.Token))
	if status != http.StatusForbidden {
		t.Fatalf("copy into another account's volume = %d %v, want 403", status, body)
	}
	if code := fixtures.ErrorCode(t, body); code != dataplane.CodeVolumeUnauthorized {
		t.Fatalf("copy into another account's volume code = %q, want %q", code, dataplane.CodeVolumeUnauthorized)
	}
	if entries := readDir(t, bob); len(entries) != 0 {
		t.Fatalf("the other account's volume holds %v after the refusal, want nothing", entries)
	}
}

// TestTC7DeleteRequiresConfirmation deletes a non-empty directory in two steps:
// the first answer states what the deletion would remove, names the entries and
// counts the subdirectories, and leaves the directory as it was; the confirmed
// answer removes it. The volume root itself is never deletable (FILE-C-5).
func TestTC7DeleteRequiresConfirmation(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "delete-data", fixtures.MemberOne, volumeapi.PhaseBound)
	root := system.VolumeDir(t, harness.Namespace, "delete-data")
	project := filepath.Join(root, "project")
	mustMkdir(t, filepath.Join(project, "nested"))
	writeFile(t, filepath.Join(project, "a.txt"), "a\n")
	writeFile(t, filepath.Join(project, "b.txt"), "b\n")
	writeFile(t, filepath.Join(project, "nested", "c.txt"), "c\n")

	window := system.OpenWindow(t, fixtures.MemberOne)

	status, body := system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "delete-data", "delete"),
		map[string]any{"path": "project"}, fixtures.Bearer(window.Token))
	if status != http.StatusConflict {
		t.Fatalf("unconfirmed delete of project = %d %v, want 409", status, body)
	}
	if code := fixtures.ErrorCode(t, body); code != dataplane.CodeDeleteUnconfirmed {
		t.Fatalf("unconfirmed delete code = %q, want %q", code, dataplane.CodeDeleteUnconfirmed)
	}
	confirmation, ok := body["confirmation"].(map[string]any)
	if !ok {
		t.Fatalf("unconfirmed delete carries no confirmation: %v", body)
	}
	if count, _ := confirmation["entryCount"].(float64); int(count) != 4 {
		t.Fatalf("confirmation entryCount = %v, want the four entries below project", confirmation["entryCount"])
	}
	if count, _ := confirmation["subdirectoryCount"].(float64); int(count) != 1 {
		t.Fatalf("confirmation subdirectoryCount = %v, want the one subdirectory below project", confirmation["subdirectoryCount"])
	}
	assertEntriesListed(t, confirmation, "a.txt", "b.txt", "nested")
	if reason := fixtures.ErrorMessage(t, body); !strings.Contains(reason, "确认") {
		t.Fatalf("unconfirmed delete reason = %q, want it to ask for confirmation", reason)
	}
	// Nothing was removed before the confirmation.
	for name, content := range map[string]string{"a.txt": "a\n", "b.txt": "b\n", "nested/c.txt": "c\n"} {
		assertContent(t, filepath.Join(project, name), content)
	}

	status, body = system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "delete-data", "delete"),
		map[string]any{"path": "project", "confirm": true}, fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("confirmed delete of project = %d %v, want 200", status, body)
	}
	if _, err := os.Lstat(project); !os.IsNotExist(err) {
		t.Fatalf("project still exists after the confirmed delete: %v", err)
	}

	status, body = system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "delete-data", "delete"),
		map[string]any{"path": "", "confirm": true}, fixtures.Bearer(window.Token))
	if status != http.StatusBadRequest {
		t.Fatalf("delete of the volume root = %d %v, want 400", status, body)
	}
	if code := fixtures.ErrorCode(t, body); code != dataplane.CodeVolumeRootProtected {
		t.Fatalf("volume root delete code = %q, want %q", code, dataplane.CodeVolumeRootProtected)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("the volume root is gone after the refused delete: %v", err)
	}
}

// TestTC7FailedCopyLeavesNoHalfResult copies a file into a directory its owner
// cannot write. The copy is refused with a reason, the source keeps its content,
// and the target directory holds nothing at all: no partial file, no staging
// leftover (FILE-C-5).
func TestTC7FailedCopyLeavesNoHalfResult(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "source-data", fixtures.MemberOne, volumeapi.PhaseBound)
	system.CreateVolume(t, "target-data", fixtures.MemberOne, volumeapi.PhaseBound)
	source := system.VolumeDir(t, harness.Namespace, "source-data")
	target := system.VolumeDir(t, harness.Namespace, "target-data")
	writeFile(t, filepath.Join(source, "payload.bin"), "payload bytes\n")
	locked := filepath.Join(target, "locked")
	mustMkdir(t, locked)

	window := system.OpenWindow(t, fixtures.MemberOne)

	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatalf("make the target directory read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	status, body := system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "source-data", "copy"),
		map[string]any{"path": "payload.bin", "target": map[string]any{
			"namespace": harness.Namespace, "name": "target-data", "path": "locked/payload.bin",
		}}, fixtures.Bearer(window.Token))
	if status == http.StatusOK {
		t.Fatalf("copy into a read-only directory = %d %v, want a refusal", status, body)
	}
	if reason := fixtures.ErrorMessage(t, body); reason == "" {
		t.Fatalf("copy into a read-only directory carries no reason: %v", body)
	}
	assertContent(t, filepath.Join(source, "payload.bin"), "payload bytes\n")
	if entries := readDir(t, locked); len(entries) != 0 {
		t.Fatalf("the read-only directory holds %v after the failed copy, want nothing", entries)
	}
	if entries := readDir(t, target); len(entries) != 1 || entries[0].Name() != "locked" {
		t.Fatalf("the target volume holds %v after the failed copy, want only locked", entries)
	}
}

// TestTC7CopyInsideOneVolumeKeepsTheSource copies one file inside its own
// volume: the copy is a second entry with the source's content, and the source
// stays at its path. The window's default target volume is the source volume,
// so this is the path a reader takes without changing the picker (FILE-C-5).
func TestTC7CopyInsideOneVolumeKeepsTheSource(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "same-volume", fixtures.MemberOne, volumeapi.PhaseBound)
	root := system.VolumeDir(t, harness.Namespace, "same-volume")
	writeFile(t, filepath.Join(root, "notes.txt"), "notes body\n")

	window := system.OpenWindow(t, fixtures.MemberOne)

	status, body := system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "same-volume", "copy"),
		map[string]any{"path": "notes.txt", "target": map[string]any{
			"namespace": harness.Namespace, "name": "same-volume", "path": "copy.txt",
		}}, fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("copy notes.txt inside its own volume = %d %v, want 200", status, body)
	}
	if operation, _ := body["operation"].(string); operation != "copy" {
		t.Fatalf("answer names operation %q, want copy", operation)
	}
	// Two entries, each with the source's content: the copy did not turn into
	// a move.
	assertContent(t, filepath.Join(root, "notes.txt"), "notes body\n")
	assertContent(t, filepath.Join(root, "copy.txt"), "notes body\n")
	if entries := readDir(t, root); len(entries) != 2 {
		t.Fatalf("the volume holds %d entries after the copy, want two", len(entries))
	}
	assertNoStaging(t, root)
}

// TestTC7MoveThatCannotEmptyItsSourceChangesNothing moves a directory whose
// subtree holds a directory the process cannot write. The move is refused
// before the copy lands, so the source keeps every entry and the target volume
// holds nothing: neither end is left in part (FILE-C-5).
func TestTC7MoveThatCannotEmptyItsSourceChangesNothing(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "move-source", fixtures.MemberOne, volumeapi.PhaseBound)
	system.CreateVolume(t, "move-target", fixtures.MemberOne, volumeapi.PhaseBound)
	source := system.VolumeDir(t, harness.Namespace, "move-source")
	target := system.VolumeDir(t, harness.Namespace, "move-target")
	mustMkdir(t, filepath.Join(source, "bundle", "locked"))
	writeFile(t, filepath.Join(source, "bundle", "report.txt"), "report body\n")
	writeFile(t, filepath.Join(source, "bundle", "locked", "inner.txt"), "inner body\n")
	lockDir(t, filepath.Join(source, "bundle", "locked"))

	window := system.OpenWindow(t, fixtures.MemberOne)

	status, body := system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "move-source", "move"),
		map[string]any{"path": "bundle", "target": map[string]any{
			"namespace": harness.Namespace, "name": "move-target", "path": "bundle",
		}}, fixtures.Bearer(window.Token))
	if status != http.StatusConflict {
		t.Fatalf("move of a source that cannot be emptied = %d %v, want 409", status, body)
	}
	if code := fixtures.ErrorCode(t, body); code != dataplane.CodePathNotRemovable {
		t.Fatalf("move refusal code = %q, want %q", code, dataplane.CodePathNotRemovable)
	}
	reason := fixtures.ErrorMessage(t, body)
	if !strings.Contains(reason, "locked") || !strings.Contains(reason, "未改动") {
		t.Fatalf("move refusal reason = %q, want it to name the blocking directory and the untouched ends", reason)
	}
	// The source keeps every entry and the target holds nothing at all.
	assertContent(t, filepath.Join(source, "bundle", "report.txt"), "report body\n")
	assertContent(t, filepath.Join(source, "bundle", "locked", "inner.txt"), "inner body\n")
	if entries := readDir(t, target); len(entries) != 0 {
		t.Fatalf("the target volume holds %v after the refused move, want nothing", entries)
	}
	assertNoStaging(t, source)
	assertNoStaging(t, target)
}

// TestTC7DeleteThatCannotEmptyItsTargetChangesNothing confirms the deletion of
// a directory whose subtree holds a directory the process cannot write. The
// deletion does not start, the reason says so, and every entry is still there:
// the answer never claims a state the volume is not in (FILE-C-5).
func TestTC7DeleteThatCannotEmptyItsTargetChangesNothing(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "delete-locked", fixtures.MemberOne, volumeapi.PhaseBound)
	root := system.VolumeDir(t, harness.Namespace, "delete-locked")
	project := filepath.Join(root, "project")
	mustMkdir(t, filepath.Join(project, "locked"))
	writeFile(t, filepath.Join(project, "a.txt"), "a\n")
	writeFile(t, filepath.Join(project, "locked", "inner.txt"), "inner body\n")
	lockDir(t, filepath.Join(project, "locked"))

	window := system.OpenWindow(t, fixtures.MemberOne)

	status, body := system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "delete-locked", "delete"),
		map[string]any{"path": "project", "confirm": true}, fixtures.Bearer(window.Token))
	if status != http.StatusConflict {
		t.Fatalf("confirmed delete of a directory that cannot be emptied = %d %v, want 409", status, body)
	}
	if code := fixtures.ErrorCode(t, body); code != dataplane.CodePathNotRemovable {
		t.Fatalf("delete refusal code = %q, want %q", code, dataplane.CodePathNotRemovable)
	}
	reason := fixtures.ErrorMessage(t, body)
	if !strings.Contains(reason, "locked") || !strings.Contains(reason, "未删除任何内容") {
		t.Fatalf("delete refusal reason = %q, want it to name the blocking directory and that nothing was removed", reason)
	}
	// Nothing was removed before the refusal.
	assertContent(t, filepath.Join(project, "a.txt"), "a\n")
	assertContent(t, filepath.Join(project, "locked", "inner.txt"), "inner body\n")
	assertNoStaging(t, root)
}

// TestTC7FailedDirectoryCopyLeavesNoStaging copies a directory whose first
// subtree is a read-only, non-empty directory and whose second one holds a file
// the copy cannot read. The staged subtree already carries the source's
// read-only mode when the copy fails, so clearing it has to restore the
// directories first: the copy is refused, the source keeps every entry, and the
// target volume holds no staging leftover (FILE-C-5).
func TestTC7FailedDirectoryCopyLeavesNoStaging(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "residue-source", fixtures.MemberOne, volumeapi.PhaseBound)
	system.CreateVolume(t, "residue-target", fixtures.MemberOne, volumeapi.PhaseBound)
	source := system.VolumeDir(t, harness.Namespace, "residue-source")
	target := system.VolumeDir(t, harness.Namespace, "residue-target")
	top := filepath.Join(source, "top")
	// Names arrange the order: entries are copied in name order, so keeper is
	// staged to completion (and takes its read-only mode) before zfail fails.
	mustMkdir(t, filepath.Join(top, "keeper"))
	writeFile(t, filepath.Join(top, "keeper", "x.txt"), "x\n")
	lockDir(t, filepath.Join(top, "keeper"))
	mustMkdir(t, filepath.Join(top, "zfail"))
	badFile := filepath.Join(top, "zfail", "bad.txt")
	writeFile(t, badFile, "bad\n")
	if err := os.Chmod(badFile, 0o000); err != nil {
		t.Fatalf("make %s unreadable: %v", badFile, err)
	}

	window := system.OpenWindow(t, fixtures.MemberOne)

	status, body := system.DataPlaneJSON(t, http.MethodPost, changePath(harness.Namespace, "residue-source", "copy"),
		map[string]any{"path": "top", "target": map[string]any{
			"namespace": harness.Namespace, "name": "residue-target", "path": "top",
		}}, fixtures.Bearer(window.Token))
	if status != http.StatusInternalServerError {
		t.Fatalf("copy of a tree holding an unreadable file = %d %v, want 500", status, body)
	}
	if code := fixtures.ErrorCode(t, body); code != dataplane.CodeFilesUnreadable {
		t.Fatalf("failed directory copy code = %q, want %q", code, dataplane.CodeFilesUnreadable)
	}
	// The refusal names the file the copy could not read, so this case reached
	// the failure after the read-only subtree was staged.
	if reason := fixtures.ErrorMessage(t, body); !strings.Contains(reason, "top/zfail/bad.txt") {
		t.Fatalf("failed directory copy reason = %q, want it to name top/zfail/bad.txt", reason)
	}

	// The source keeps every entry and its content.
	assertContent(t, filepath.Join(top, "keeper", "x.txt"), "x\n")
	if err := os.Chmod(badFile, 0o644); err != nil {
		t.Fatalf("restore read access to %s: %v", badFile, err)
	}
	assertContent(t, badFile, "bad\n")

	// The target volume holds nothing at all: no half result and no staging
	// entry left behind.
	if entries := readDir(t, target); len(entries) != 0 {
		t.Fatalf("the target volume holds %v after the failed copy, want nothing", entries)
	}
	assertNoStaging(t, target)
}

// changePath names one change operation of one volume (FILE-C-5).
func changePath(namespace, name, operation string) string {
	return "/files/volumes/" + namespace + "/" + name + "/" + operation
}

// writeFile writes one file below a served volume directory.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// mustMkdir creates one directory below a served volume directory.
func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
}

// lockDir makes one directory unwritable for the rest of the case, so a removal
// of a tree that holds it cannot finish.
func lockDir(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o555); err != nil {
		t.Fatalf("make %s read-only: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o755) })
}

// assertNoStaging checks that a served directory holds no staging leftover.
func assertNoStaging(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".kos-partial-") {
			t.Fatalf("%s holds the staging leftover %s", root, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

// assertContent checks one file's bytes on disk.
func assertContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(content) != want {
		t.Fatalf("%s holds %q, want %q", path, content, want)
	}
}

// assertEntriesListed checks that one confirmation names the given entries.
func assertEntriesListed(t *testing.T, confirmation map[string]any, want ...string) {
	t.Helper()
	raw, ok := confirmation["entries"].([]any)
	if !ok {
		t.Fatalf("confirmation lists no entries: %v", confirmation)
	}
	listed := make(map[string]bool, len(raw))
	for _, item := range raw {
		name, _ := item.(string)
		listed[name] = true
	}
	for _, name := range want {
		if !listed[name] {
			t.Fatalf("confirmation lists %v, want it to name %s", raw, name)
		}
	}
}

// readDir lists one directory's entries.
func readDir(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read the directory %s: %v", path, err)
	}
	return entries
}
