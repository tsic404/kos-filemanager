// Package filetc6 checks FILE-TC-6: an upload lands in the directory the window
// is browsing, its progress is readable while it runs and ends at the file's
// total size, a same-name file is never replaced before the window confirms and
// is unchanged after cancelling, a cancelled or failed session leaves no partial
// file at the target, and a download of a missing or unreadable file is refused
// with a reason instead of empty content (FILE-AC-4, FILE-C-4).
package filetc6

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	volumeapi "github.com/tsic404/kosd/pkg/volume"
	"github.com/tsic404/kosd/pkg/write"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-filemanager/pkg/dataplane"
	"github.com/tsic404/kos-filemanager/tests/acceptance/fixtures"
)

// volume is the volume the case browses and transfers inside.
const volume = "transfer-data"

// start boots the product and opens one window on a bound volume of its own.
func start(t *testing.T) (*fixtures.Case, string, string) {
	t.Helper()
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, volume, fixtures.MemberOne, volumeapi.PhaseBound)
	root := system.VolumeDir(t, harness.Namespace, volume)
	window := system.OpenWindow(t, fixtures.MemberOne)
	return system, root, window.Token
}

// transfersPath is the session collection of the case's volume.
func transfersPath() string {
	return "/files/volumes/" + harness.Namespace + "/" + volume + "/transfers"
}

// transferPath is one session of the case's volume.
func transferPath(id string) string { return transfersPath() + "/" + id }

// contentPath is the content of one session.
func contentPath(id string) string { return transferPath(id) + "/content" }

// downloadPath is one download of the case's volume.
func downloadPath(relative string) string {
	return "/files/volumes/" + harness.Namespace + "/" + volume + "/download?path=" + relative
}

// openSession asks the data plane to start one upload and returns its answer.
func openSession(t *testing.T, system *fixtures.Case, token, directory, name string, size int, overwrite bool) (int, map[string]any) {
	t.Helper()
	request, err := json.Marshal(map[string]any{
		"path": directory, "name": name, "size": size, "overwrite": overwrite,
	})
	if err != nil {
		t.Fatalf("encode the upload request: %v", err)
	}
	return system.DataPlaneSend(t, http.MethodPost, transfersPath(), fixtures.Bearer(token), string(request))
}

// uploadID reads one session's identity from its answer.
func uploadID(t *testing.T, answer map[string]any) string {
	t.Helper()
	id, _ := answer["id"].(string)
	if id == "" {
		t.Fatalf("answer names no transfer: %v", answer)
	}
	return id
}

// progress reads one session's state as the window shows it.
func progress(t *testing.T, system *fixtures.Case, token, id string) map[string]any {
	t.Helper()
	status, answer := system.DataPlane(t, http.MethodGet, transferPath(id), fixtures.Bearer(token))
	if status != http.StatusOK {
		t.Fatalf("read the progress of %s = %d %v, want 200", id, status, answer)
	}
	return answer
}

// assertNoPartial checks that no file stands at the target and that the
// staging area kept no bytes of the session.
func assertNoPartial(t *testing.T, root, target string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, target)); !os.IsNotExist(err) {
		t.Fatalf("target %s exists after the session ended unsuccessfully: %v", target, err)
	}
	assertStagingEmpty(t, root)
}

// assertStagingEmpty checks that the volume's staging area holds no bytes.
func assertStagingEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, dataplane.StagingDir))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read the staging area: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("staging area holds %v after the session ended, want nothing", names)
	}
}

// TestTC6UploadLandsInBrowsedDirectoryWithProgress walks one upload through two
// chunks: the progress read between them names half the content, the target
// does not exist until the whole content arrived, and afterwards the file holds
// exactly the source bytes in the directory the window was browsing.
func TestTC6UploadLandsInBrowsedDirectoryWithProgress(t *testing.T) {
	system, root, token := start(t)
	source := strings.Repeat("upload-line\n", 400)

	status, opened := openSession(t, system, token, "", "report.txt", len(source), false)
	if status != http.StatusCreated {
		t.Fatalf("open the upload = %d %v, want 201", status, opened)
	}
	if state, _ := opened["state"].(string); state != dataplane.TransferStateOpen {
		t.Fatalf("upload state = %v, want %q", opened["state"], dataplane.TransferStateOpen)
	}
	if target, _ := opened["target"].(string); target != "report.txt" {
		t.Fatalf("upload target = %v, want report.txt", opened["target"])
	}
	id := uploadID(t, opened)

	half := len(source) / 2
	status, answer := system.DataPlaneSend(t, http.MethodPut, contentPath(id),
		fixtures.Bearer(token), source[:half])
	if status != http.StatusAccepted {
		t.Fatalf("first chunk = %d %v, want 202", status, answer)
	}
	// Progress is readable while the session runs and names the file's total.
	running := progress(t, system, token, id)
	if received, _ := running["received"].(float64); int(received) != half {
		t.Fatalf("progress received = %v, want %d", running["received"], half)
	}
	if total, _ := running["size"].(float64); int(total) != len(source) {
		t.Fatalf("progress total = %v, want %d", running["size"], len(source))
	}
	if state, _ := running["state"].(string); state != dataplane.TransferStateOpen {
		t.Fatalf("progress state = %v, want %q", running["state"], dataplane.TransferStateOpen)
	}
	if _, err := os.Stat(filepath.Join(root, "report.txt")); !os.IsNotExist(err) {
		t.Fatalf("target exists before the content is whole: %v", err)
	}

	status, completed := system.DataPlaneSend(t, http.MethodPut, contentPath(id),
		fixtures.Bearer(token), source[half:])
	if status != http.StatusOK {
		t.Fatalf("last chunk = %d %v, want 200", status, completed)
	}
	if state, _ := completed["state"].(string); state != dataplane.TransferStateCompleted {
		t.Fatalf("completed state = %v, want %q", completed["state"], dataplane.TransferStateCompleted)
	}
	if received, _ := completed["received"].(float64); int(received) != len(source) {
		t.Fatalf("completed progress = %v, want the file's total %d", completed["received"], len(source))
	}
	written, err := os.ReadFile(filepath.Join(root, "report.txt"))
	if err != nil {
		t.Fatalf("read the uploaded file: %v", err)
	}
	if string(written) != source {
		t.Fatalf("uploaded file = %d bytes, want the %d source bytes", len(written), len(source))
	}

	// The browse of the directory the window was browsing lists the new file.
	status, listing := system.DataPlane(t, http.MethodGet,
		"/files/volumes/"+harness.Namespace+"/"+volume+"/entries", fixtures.Bearer(token))
	if status != http.StatusOK {
		t.Fatalf("browse the volume root = %d %v, want 200", status, listing)
	}
	listed, found := entryByName(t, listing, "report.txt")
	if !found {
		t.Fatalf("browse lists %v, want report.txt", entryNames(t, listing))
	}
	if size, _ := listed["size"].(float64); int64(size) != int64(len(source)) {
		t.Fatalf("listed report.txt size = %v, want %d", listed["size"], len(source))
	}

	// An upload addressed at a browsed subdirectory writes there, not at the root.
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	status, opened = openSession(t, system, token, "docs", "notes.txt", 5, false)
	if status != http.StatusCreated {
		t.Fatalf("open the subdirectory upload = %d %v, want 201", status, opened)
	}
	id = uploadID(t, opened)
	if status, answer := system.DataPlaneSend(t, http.MethodPut, contentPath(id),
		fixtures.Bearer(token), "notes"); status != http.StatusOK {
		t.Fatalf("subdirectory upload = %d %v, want 200", status, answer)
	}
	if got, err := os.ReadFile(filepath.Join(root, "docs", "notes.txt")); err != nil || string(got) != "notes" {
		t.Fatalf("subdirectory upload wrote %q %v, want notes in docs", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes.txt")); !os.IsNotExist(err) {
		t.Fatalf("subdirectory upload also wrote the volume root: %v", err)
	}
}

// TestTC6ConflictWaitsForConfirmationAndSurvivesCancel uploads a file whose name
// the volume already holds: the target keeps its content until the window
// confirms, stays unchanged when the confirmation is cancelled, and a file that
// appears while the transfer runs is reported rather than replaced.
func TestTC6ConflictWaitsForConfirmationAndSurvivesCancel(t *testing.T) {
	system, root, token := start(t)
	existing := "existing-content\n"
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte(existing), 0o644); err != nil {
		t.Fatalf("write the existing file: %v", err)
	}
	replacement := "replacement-content\n"

	status, opened := openSession(t, system, token, "", "notes.txt", len(replacement), false)
	if status != http.StatusCreated {
		t.Fatalf("open the conflicting upload = %d %v, want 201", status, opened)
	}
	if state, _ := opened["state"].(string); state != dataplane.TransferStateConflict {
		t.Fatalf("conflicting session state = %v, want %q", opened["state"], dataplane.TransferStateConflict)
	}
	conflict, _ := opened["conflict"].(map[string]any)
	if size, _ := conflict["size"].(float64); int(size) != len(existing) {
		t.Fatalf("conflict size = %v, want the existing file's %d bytes", conflict["size"], len(existing))
	}
	if reason, _ := opened["reason"].(string); !strings.Contains(reason, "同名文件") {
		t.Fatalf("conflict reason = %q, want it to name the same-name file", reason)
	}
	id := uploadID(t, opened)

	status, refused := system.DataPlaneSend(t, http.MethodPut, contentPath(id),
		fixtures.Bearer(token), replacement)
	if status != http.StatusConflict {
		t.Fatalf("content before confirming = %d %v, want 409", status, refused)
	}
	if code := fixtures.ErrorCode(t, refused); code != dataplane.CodeUploadConflict {
		t.Fatalf("content before confirming code = %q, want %q", code, dataplane.CodeUploadConflict)
	}
	assertFileContent(t, filepath.Join(root, "notes.txt"), existing)

	// Cancelling the confirmation leaves the target exactly as it was.
	status, cancelled := system.DataPlane(t, http.MethodDelete, transferPath(id), fixtures.Bearer(token))
	if status != http.StatusOK {
		t.Fatalf("cancel the conflicting session = %d %v, want 200", status, cancelled)
	}
	if state, _ := cancelled["state"].(string); state != dataplane.TransferStateCancelled {
		t.Fatalf("cancelled state = %v, want %q", cancelled["state"], dataplane.TransferStateCancelled)
	}
	assertFileContent(t, filepath.Join(root, "notes.txt"), existing)
	assertStagingEmpty(t, root)

	// A file that appears while unchecked content is running is not replaced.
	status, opened = openSession(t, system, token, "", "late.txt", 10, false)
	if status != http.StatusCreated {
		t.Fatalf("open the late upload = %d %v, want 201", status, opened)
	}
	id = uploadID(t, opened)
	if status, answer := system.DataPlaneSend(t, http.MethodPut, contentPath(id),
		fixtures.Bearer(token), "12345"); status != http.StatusAccepted {
		t.Fatalf("partial late upload = %d %v, want 202", status, answer)
	}
	appeared := "appeared-during-transfer\n"
	if err := os.WriteFile(filepath.Join(root, "late.txt"), []byte(appeared), 0o644); err != nil {
		t.Fatalf("write the file that appeared: %v", err)
	}
	status, refused = system.DataPlaneSend(t, http.MethodPut, contentPath(id),
		fixtures.Bearer(token), "67890")
	if status != http.StatusConflict {
		t.Fatalf("content past a file that appeared = %d %v, want 409", status, refused)
	}
	assertFileContent(t, filepath.Join(root, "late.txt"), appeared)
	entries, err := os.ReadDir(filepath.Join(root, dataplane.StagingDir))
	if err == nil && len(entries) != 0 {
		t.Fatalf("staging area holds %d entries after the conflict", len(entries))
	}

	// With the confirmation the same name is replaced by the uploaded bytes.
	status, confirmed := openSession(t, system, token, "", "notes.txt", len(replacement), true)
	if status != http.StatusCreated {
		t.Fatalf("open the confirmed upload = %d %v, want 201", status, confirmed)
	}
	if overwrite, _ := confirmed["overwrite"].(bool); !overwrite {
		t.Fatalf("confirmed session does not report the confirmation: %v", confirmed)
	}
	id = uploadID(t, confirmed)
	if status, answer := system.DataPlaneSend(t, http.MethodPut, contentPath(id),
		fixtures.Bearer(token), replacement); status != http.StatusOK {
		t.Fatalf("confirmed upload = %d %v, want 200", status, answer)
	}
	assertFileContent(t, filepath.Join(root, "notes.txt"), replacement)
}

// TestTC6CancelAndMidFailureLeaveNoPartialFile cancels one running upload and
// breaks another past its declared size: neither leaves a file at the target,
// neither leaves staged bytes, and the volume's other files are untouched.
func TestTC6CancelAndMidFailureLeaveNoPartialFile(t *testing.T) {
	system, root, token := start(t)
	seed := "untouched\n"
	if err := os.WriteFile(filepath.Join(root, "seed.txt"), []byte(seed), 0o644); err != nil {
		t.Fatalf("write the seed file: %v", err)
	}
	source := strings.Repeat("x", 4096)

	status, opened := openSession(t, system, token, "", "large.bin", len(source), false)
	if status != http.StatusCreated {
		t.Fatalf("open the running upload = %d %v, want 201", status, opened)
	}
	id := uploadID(t, opened)
	if status, answer := system.DataPlaneSend(t, http.MethodPut, contentPath(id),
		fixtures.Bearer(token), source[:1024]); status != http.StatusAccepted {
		t.Fatalf("partial chunk = %d %v, want 202", status, answer)
	}
	if received, _ := progress(t, system, token, id)["received"].(float64); int(received) != 1024 {
		t.Fatalf("running progress = %v, want 1024", received)
	}
	status, cancelled := system.DataPlane(t, http.MethodDelete, transferPath(id), fixtures.Bearer(token))
	if status != http.StatusOK {
		t.Fatalf("cancel the running upload = %d %v, want 200", status, cancelled)
	}
	if state, _ := cancelled["state"].(string); state != dataplane.TransferStateCancelled {
		t.Fatalf("cancelled state = %v, want %q", cancelled["state"], dataplane.TransferStateCancelled)
	}
	assertNoPartial(t, root, "large.bin")

	// A session that received more than it declared fails and cleans up too.
	status, opened = openSession(t, system, token, "", "overrun.bin", 4, false)
	if status != http.StatusCreated {
		t.Fatalf("open the failing upload = %d %v, want 201", status, opened)
	}
	id = uploadID(t, opened)
	status, failed := system.DataPlaneSend(t, http.MethodPut, contentPath(id),
		fixtures.Bearer(token), "123456789")
	if status != http.StatusConflict {
		t.Fatalf("oversized chunk = %d %v, want 409", status, failed)
	}
	if code := fixtures.ErrorCode(t, failed); code != dataplane.CodeTransferSizeMismatch {
		t.Fatalf("oversized chunk code = %q, want %q", code, dataplane.CodeTransferSizeMismatch)
	}
	if state := nestedString(t, failed, "transfer", "state"); state != dataplane.TransferStateFailed {
		t.Fatalf("oversized session state = %q, want %q", state, dataplane.TransferStateFailed)
	}
	assertNoPartial(t, root, "overrun.bin")

	// The volume's own file is exactly as it was before either session ran.
	assertFileContent(t, filepath.Join(root, "seed.txt"), seed)
}

// TestTC6DownloadRefusesMissingAndUnreadable checks FILE-C-4's download refusals:
// a missing file and an unreadable one are both refused with a reason and no
// file stream, while a readable file streams whole with its size declared.
func TestTC6DownloadRefusesMissingAndUnreadable(t *testing.T) {
	system, root, token := start(t)
	content := "downloadable-content\n"
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("write the download file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("create docs: %v", err)
	}

	status, headers, body := system.DataPlaneRaw(t, http.MethodGet, downloadPath("missing.txt"),
		fixtures.Bearer(token), "")
	if status != http.StatusNotFound {
		t.Fatalf("download of a missing file = %d %s, want 404", status, body)
	}
	if code := fixtures.ErrorCode(t, decode(t, body)); code != dataplane.CodePathNotFound {
		t.Fatalf("missing download code = %q, want %q", code, dataplane.CodePathNotFound)
	}
	if reason := fixtures.ErrorMessage(t, decode(t, body)); !strings.Contains(reason, "missing.txt") {
		t.Fatalf("missing download reason = %q, want it to name the file", reason)
	}
	if disposition := headers.Get("Content-Disposition"); disposition != "" {
		t.Fatalf("missing download carries Content-Disposition %q, want a refusal", disposition)
	}

	status, headers, body = system.DataPlaneRaw(t, http.MethodGet, downloadPath("docs"),
		fixtures.Bearer(token), "")
	if status != http.StatusBadRequest {
		t.Fatalf("download of a directory = %d %s, want 400", status, body)
	}
	if reason := fixtures.ErrorMessage(t, decode(t, body)); !strings.Contains(reason, "目录") {
		t.Fatalf("directory download reason = %q, want it to name the directory", reason)
	}
	if disposition := headers.Get("Content-Disposition"); disposition != "" {
		t.Fatalf("directory download carries Content-Disposition %q, want a refusal", disposition)
	}

	// An unreadable file is refused with its reason instead of empty content.
	unreadable := filepath.Join(root, "locked.txt")
	if err := os.WriteFile(unreadable, []byte("locked-content\n"), 0o644); err != nil {
		t.Fatalf("write the locked file: %v", err)
	}
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatalf("make the file unreadable: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o644) })
	if os.Geteuid() == 0 {
		t.Log("running as root: the unreadable file stays readable, skipping that half")
	} else {
		status, headers, body = system.DataPlaneRaw(t, http.MethodGet, downloadPath("locked.txt"),
			fixtures.Bearer(token), "")
		if status != http.StatusForbidden {
			t.Fatalf("download of an unreadable file = %d %s, want 403", status, body)
		}
		if code := fixtures.ErrorCode(t, decode(t, body)); code != dataplane.CodeFileUnreadable {
			t.Fatalf("unreadable download code = %q, want %q", code, dataplane.CodeFileUnreadable)
		}
		if reason := fixtures.ErrorMessage(t, decode(t, body)); !strings.Contains(reason, "不可读") {
			t.Fatalf("unreadable download reason = %q, want it to name the file as unreadable", reason)
		}
		if disposition := headers.Get("Content-Disposition"); disposition != "" {
			t.Fatalf("unreadable download carries Content-Disposition %q, want a refusal", disposition)
		}
	}
	if err := os.Chmod(unreadable, 0o644); err != nil {
		t.Fatalf("restore the locked file: %v", err)
	}
	assertFileContent(t, unreadable, "locked-content\n")

	// A readable file streams whole: the declared size is the file's total and
	// the bytes are the file's own.
	status, headers, body = system.DataPlaneRaw(t, http.MethodGet, downloadPath("notes.txt"),
		fixtures.Bearer(token), "")
	if status != http.StatusOK {
		t.Fatalf("download of a readable file = %d %s, want 200", status, body)
	}
	if declared := headers.Get("Content-Length"); declared != strconv.Itoa(len(content)) {
		t.Fatalf("download length = %q, want %d", declared, len(content))
	}
	if string(body) != content {
		t.Fatalf("download body = %q, want %q", body, content)
	}
	if disposition := headers.Get("Content-Disposition"); !strings.HasPrefix(disposition, "attachment") {
		t.Fatalf("download Content-Disposition = %q, want an attachment", disposition)
	}
	assertFileContent(t, filepath.Join(root, "notes.txt"), content)
}

// decode reads one refusal answer the helpers read fields from.
func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	answer := map[string]any{}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("decode the refusal %q: %v", body, err)
	}
	return answer
}

// nestedString reads one string below an object key of one answer.
func nestedString(t *testing.T, body map[string]any, keys ...string) string {
	t.Helper()
	var current any = body
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("answer carries no object %q: %v", key, body)
		}
		current = object[key]
	}
	value, _ := current.(string)
	return value
}

// entryNames lists the entry names of one listing answer.
func entryNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["entries"].([]any)
	if !ok {
		t.Fatalf("answer carries no entries: %v", body)
	}
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("entry is not an object: %v", item)
		}
		name, _ := entry["name"].(string)
		names = append(names, name)
	}
	return names
}

// entryByName reads one entry of a listing answer.
func entryByName(t *testing.T, body map[string]any, name string) (map[string]any, bool) {
	t.Helper()
	raw, ok := body["entries"].([]any)
	if !ok {
		t.Fatalf("answer carries no entries: %v", body)
	}
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if listed, _ := entry["name"].(string); listed == name {
			return entry, true
		}
	}
	return nil, false
}

// assertFileContent compares one file with the content it must hold.
func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
