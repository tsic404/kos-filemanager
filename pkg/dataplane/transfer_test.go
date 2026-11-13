package dataplane_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	volumeapi "github.com/tsic404/kosd/pkg/volume"

	"github.com/tsic404/kos-filemanager/pkg/dataplane"
)

// transferCall sends one request with a body and decodes the JSON answer.
func transferCall(t *testing.T, plane *dataPlane, method, path, body string) (int, map[string]any) {
	t.Helper()
	status, _, answer := transferResponse(t, plane, method, path, body)
	return status, answer
}

// transferResponse sends one request with a body and reports the answer whole.
func transferResponse(t *testing.T, plane *dataPlane, method, path, body string) (int, http.Header, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(method, plane.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer alice-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("call %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	answer := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		t.Fatalf("decode the answer of %s %s: %v", method, path, err)
	}
	return response.StatusCode, response.Header, answer
}

// openUpload starts one upload session and returns its identity.
func openUpload(t *testing.T, plane *dataPlane, directory, name string, size int, overwrite bool) (int, map[string]any) {
	t.Helper()
	request, err := json.Marshal(map[string]any{
		"path": directory, "name": name, "size": size, "overwrite": overwrite,
	})
	if err != nil {
		t.Fatalf("encode the session request: %v", err)
	}
	return transferCall(t, plane, http.MethodPost, "/files/volumes/team/bound/transfers", string(request))
}

// stagedFiles lists the files in the volume's staging area.
func stagedFiles(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, dataplane.StagingDir))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the staging area: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// TestUploadWritesIntoBrowsedDirectoryWithProgress checks FILE-C-4's upload
// path: content goes into the browsed directory, the received count is readable
// between chunks, and it equals the declared size once the session completed.
func TestUploadWritesIntoBrowsedDirectoryWithProgress(t *testing.T) {
	base := t.TempDir()
	root := serveVolume(t, base, "team", "bound", "seed.txt", "seed")
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	source := &volumes{observed: []dataplane.ObservedVolume{
		volume("bound", account, volumeapi.PhaseBound, "node-a", clock),
	}}
	plane := startConfigured(t, dataplane.Config{Volumes: source, Roots: dataplane.DirectoryRoots{Base: base}})

	content := "abcdefghij"
	status, opened := openUpload(t, plane, "docs", "report.txt", len(content), false)
	if status != http.StatusCreated {
		t.Fatalf("open the upload = %d %v, want 201", status, opened)
	}
	if state, _ := opened["state"].(string); state != dataplane.TransferStateOpen {
		t.Fatalf("upload state = %v, want %q", opened["state"], dataplane.TransferStateOpen)
	}
	id, _ := opened["id"].(string)
	if id == "" {
		t.Fatalf("upload opened without an identity: %v", opened)
	}
	if target, _ := opened["target"].(string); target != "docs/report.txt" {
		t.Fatalf("upload target = %v, want docs/report.txt", opened["target"])
	}

	if status, answer := transferCall(t, plane, http.MethodPut,
		"/files/volumes/team/bound/transfers/"+id+"/content", content[:4]); status != http.StatusAccepted {
		t.Fatalf("first chunk = %d %v, want 202", status, answer)
	}
	// Mid-transfer progress is readable and names the volume's own total.
	status, progress := transferCall(t, plane, http.MethodGet, "/files/volumes/team/bound/transfers/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("read the progress = %d %v, want 200", status, progress)
	}
	if received, _ := progress["received"].(float64); int(received) != 4 {
		t.Fatalf("progress received = %v, want 4", progress["received"])
	}
	if total, _ := progress["size"].(float64); int(total) != len(content) {
		t.Fatalf("progress size = %v, want %d", progress["size"], len(content))
	}
	if state, _ := progress["state"].(string); state != dataplane.TransferStateOpen {
		t.Fatalf("progress state = %v, want %q", progress["state"], dataplane.TransferStateOpen)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "report.txt")); !os.IsNotExist(err) {
		t.Fatalf("the target name exists before the content is whole: %v", err)
	}

	status, completed := transferCall(t, plane, http.MethodPut,
		"/files/volumes/team/bound/transfers/"+id+"/content", content[4:])
	if status != http.StatusOK {
		t.Fatalf("last chunk = %d %v, want 200", status, completed)
	}
	if state, _ := completed["state"].(string); state != dataplane.TransferStateCompleted {
		t.Fatalf("completed state = %v, want %q", completed["state"], dataplane.TransferStateCompleted)
	}
	if received, _ := completed["received"].(float64); int(received) != len(content) {
		t.Fatalf("completed received = %v, want %d", completed["received"], len(content))
	}
	if _, present := completed["completedAt"]; !present {
		t.Fatalf("completed session names no completion time: %v", completed)
	}
	written, err := os.ReadFile(filepath.Join(root, "docs", "report.txt"))
	if err != nil {
		t.Fatalf("read the uploaded file: %v", err)
	}
	if string(written) != content {
		t.Fatalf("uploaded file = %q, want %q", written, content)
	}
	if staged := stagedFiles(t, root); len(staged) != 0 {
		t.Fatalf("staging area holds %v after completion, want nothing", staged)
	}

	// The browse of the directory the upload named lists the new file, and the
	// staging area is not part of the volume's content.
	status, listing := plane.callWithToken(http.MethodGet,
		"/files/volumes/team/bound/entries?path=docs", "alice-token")
	if status != http.StatusOK {
		t.Fatalf("browse docs = %d %v, want 200", status, listing)
	}
	names := entriesNames(t, listing)
	if !containsName(names, "report.txt") {
		t.Fatalf("browse docs lists %v, want report.txt", names)
	}
	status, listing = plane.callWithToken(http.MethodGet, "/files/volumes/team/bound/entries", "alice-token")
	if status != http.StatusOK {
		t.Fatalf("browse the root = %d %v, want 200", status, listing)
	}
	if containsName(entriesNames(t, listing), dataplane.StagingDir) {
		t.Fatalf("root browse exposes the staging area: %v", listing)
	}
}

// TestUploadConflictKeepsTargetUntilConfirmed checks FILE-C-4's conflict rule:
// a same-name file is reported instead of replaced, content is refused until
// the window confirms, and cancelling leaves the target exactly as it was.
func TestUploadConflictKeepsTargetUntilConfirmed(t *testing.T) {
	base := t.TempDir()
	root := serveVolume(t, base, "team", "bound", "notes.txt", "old-content")
	source := &volumes{observed: []dataplane.ObservedVolume{
		volume("bound", account, volumeapi.PhaseBound, "node-a", clock),
	}}
	plane := startConfigured(t, dataplane.Config{Volumes: source, Roots: dataplane.DirectoryRoots{Base: base}})

	status, opened := openUpload(t, plane, "", "notes.txt", 11, false)
	if status != http.StatusCreated {
		t.Fatalf("open the conflicting upload = %d %v, want 201", status, opened)
	}
	if state, _ := opened["state"].(string); state != dataplane.TransferStateConflict {
		t.Fatalf("conflicting session state = %v, want %q", opened["state"], dataplane.TransferStateConflict)
	}
	conflict, _ := opened["conflict"].(map[string]any)
	if size, _ := conflict["size"].(float64); int(size) != len("old-content") {
		t.Fatalf("conflict size = %v, want %d", conflict["size"], len("old-content"))
	}
	if reason, _ := opened["reason"].(string); !strings.Contains(reason, "同名文件") {
		t.Fatalf("conflict reason = %q, want it to name the same-name file", reason)
	}
	id, _ := opened["id"].(string)

	status, refused := transferCall(t, plane, http.MethodPut,
		"/files/volumes/team/bound/transfers/"+id+"/content", "new-content")
	if status != http.StatusConflict {
		t.Fatalf("content before confirmation = %d %v, want 409", status, refused)
	}
	if code, _ := refusalCode(refused); code != dataplane.CodeUploadConflict {
		t.Fatalf("content before confirmation code = %q, want %q", code, dataplane.CodeUploadConflict)
	}
	if got, err := os.ReadFile(filepath.Join(root, "notes.txt")); err != nil || string(got) != "old-content" {
		t.Fatalf("target after a refused upload = %q %v, want the old content", got, err)
	}

	// Cancelling the session the window asked the user about changes nothing.
	status, cancelled := transferCall(t, plane, http.MethodDelete, "/files/volumes/team/bound/transfers/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("cancel the conflicting session = %d %v, want 200", status, cancelled)
	}
	if state, _ := cancelled["state"].(string); state != dataplane.TransferStateCancelled {
		t.Fatalf("cancelled state = %v, want %q", cancelled["state"], dataplane.TransferStateCancelled)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "notes.txt")); string(got) != "old-content" {
		t.Fatalf("target after cancelling the conflict = %q, want the old content", got)
	}

	// The confirmed session replaces the file, and the browse sees the new bytes.
	status, confirmed := openUpload(t, plane, "", "notes.txt", 11, true)
	if status != http.StatusCreated {
		t.Fatalf("open the confirmed upload = %d %v, want 201", status, confirmed)
	}
	if state, _ := confirmed["state"].(string); state != dataplane.TransferStateOpen {
		t.Fatalf("confirmed session state = %v, want %q", confirmed["state"], dataplane.TransferStateOpen)
	}
	if overwrite, _ := confirmed["overwrite"].(bool); !overwrite {
		t.Fatalf("confirmed session does not report the confirmation: %v", confirmed)
	}
	id, _ = confirmed["id"].(string)
	if status, answer := transferCall(t, plane, http.MethodPut,
		"/files/volumes/team/bound/transfers/"+id+"/content", "new-content"); status != http.StatusOK {
		t.Fatalf("confirmed upload = %d %v, want 200", status, answer)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "notes.txt")); string(got) != "new-content" {
		t.Fatalf("target after the confirmed upload = %q, want the new content", got)
	}
}

// TestCancelAndFailureLeaveNoPartialFile checks FILE-C-4's cleanup rule: a
// cancelled or failed session leaves the target name absent and no staged bytes
// behind.
func TestCancelAndFailureLeaveNoPartialFile(t *testing.T) {
	base := t.TempDir()
	root := serveVolume(t, base, "team", "bound", "seed.txt", "seed")
	source := &volumes{observed: []dataplane.ObservedVolume{
		volume("bound", account, volumeapi.PhaseBound, "node-a", clock),
	}}
	plane := startConfigured(t, dataplane.Config{Volumes: source, Roots: dataplane.DirectoryRoots{Base: base}})

	status, opened := openUpload(t, plane, "", "large.bin", 10, false)
	if status != http.StatusCreated {
		t.Fatalf("open the upload = %d %v, want 201", status, opened)
	}
	id, _ := opened["id"].(string)
	if status, answer := transferCall(t, plane, http.MethodPut,
		"/files/volumes/team/bound/transfers/"+id+"/content", "12345"); status != http.StatusAccepted {
		t.Fatalf("partial chunk = %d %v, want 202", status, answer)
	}
	if staged := stagedFiles(t, root); len(staged) != 1 {
		t.Fatalf("staging area holds %v while a session runs, want one staged file", staged)
	}
	if status, answer := transferCall(t, plane, http.MethodDelete,
		"/files/volumes/team/bound/transfers/"+id, ""); status != http.StatusOK {
		t.Fatalf("cancel the running upload = %d %v, want 200", status, answer)
	}
	if _, err := os.Stat(filepath.Join(root, "large.bin")); !os.IsNotExist(err) {
		t.Fatalf("cancelled upload left the target name behind: %v", err)
	}
	if staged := stagedFiles(t, root); len(staged) != 0 {
		t.Fatalf("staging area holds %v after cancelling, want nothing", staged)
	}
	// Content after cancelling is refused rather than reviving the session.
	if status, answer := transferCall(t, plane, http.MethodPut,
		"/files/volumes/team/bound/transfers/"+id+"/content", "12345"); status != http.StatusConflict {
		t.Fatalf("content after cancelling = %d %v, want 409", status, answer)
	}

	// A session that received more than it declared fails and cleans up too.
	status, opened = openUpload(t, plane, "", "overrun.bin", 4, false)
	if status != http.StatusCreated {
		t.Fatalf("open the second upload = %d %v, want 201", status, opened)
	}
	id, _ = opened["id"].(string)
	status, answer := transferCall(t, plane, http.MethodPut,
		"/files/volumes/team/bound/transfers/"+id+"/content", "123456789")
	if status != http.StatusConflict {
		t.Fatalf("oversized chunk = %d %v, want 409", status, answer)
	}
	if code, _ := refusalCode(answer); code != dataplane.CodeTransferSizeMismatch {
		t.Fatalf("oversized chunk code = %q, want %q", code, dataplane.CodeTransferSizeMismatch)
	}
	if state := nestedString(answer, "transfer", "state"); state != dataplane.TransferStateFailed {
		t.Fatalf("oversized session state = %v, want %q", state, dataplane.TransferStateFailed)
	}
	if _, err := os.Stat(filepath.Join(root, "overrun.bin")); !os.IsNotExist(err) {
		t.Fatalf("failed upload left the target name behind: %v", err)
	}
	if staged := stagedFiles(t, root); len(staged) != 0 {
		t.Fatalf("staging area holds %v after a failure, want nothing", staged)
	}
}

// TestUploadRefusesUnsafeNamesAndStagingTargets checks the names a session may
// write: a step out of the directory, an absolute name and the staging area
// itself are refused before anything is staged.
func TestUploadRefusesUnsafeNamesAndStagingTargets(t *testing.T) {
	base := t.TempDir()
	serveVolume(t, base, "team", "bound", "seed.txt", "seed")
	source := &volumes{observed: []dataplane.ObservedVolume{
		volume("bound", account, volumeapi.PhaseBound, "node-a", clock),
	}}
	plane := startConfigured(t, dataplane.Config{Volumes: source, Roots: dataplane.DirectoryRoots{Base: base}})

	for _, refused := range []struct {
		directory, name string
		code            string
	}{
		{"", "../escape.txt", dataplane.CodeTransferInvalid},
		{"", "a/b.txt", dataplane.CodeTransferInvalid},
		{"", "..", dataplane.CodeTransferInvalid},
		{"", "", dataplane.CodeTransferInvalid},
		{"", dataplane.StagingDir, dataplane.CodePathReserved},
		{dataplane.StagingDir, "staged.txt", dataplane.CodePathReserved},
		{"../outside", "x.txt", dataplane.CodePathInvalid},
	} {
		status, answer := openUpload(t, plane, refused.directory, refused.name, 4, false)
		if status != http.StatusBadRequest && status != http.StatusForbidden {
			t.Fatalf("open %q/%q = %d %v, want a refusal", refused.directory, refused.name, status, answer)
		}
		if code, _ := refusalCode(answer); code != refused.code {
			t.Fatalf("open %q/%q code = %q, want %q", refused.directory, refused.name, code, refused.code)
		}
	}
	// Browsing into the staging area is refused instead of served.
	status, answer := plane.callWithToken(http.MethodGet,
		"/files/volumes/team/bound/entries?path="+dataplane.StagingDir, "alice-token")
	if status != http.StatusForbidden {
		t.Fatalf("browse the staging area = %d %v, want 403", status, answer)
	}
	if code, _ := refusalCode(answer); code != dataplane.CodePathReserved {
		t.Fatalf("browse the staging area code = %q, want %q", code, dataplane.CodePathReserved)
	}
}

// refusalCode reads one answer's refusal code.
func refusalCode(body map[string]any) (string, bool) {
	failure, ok := body["error"].(map[string]any)
	if !ok {
		return "", false
	}
	code, _ := failure["code"].(string)
	return code, true
}

// nestedString reads one string below an object key of one answer.
func nestedString(body map[string]any, keys ...string) string {
	var current any = body
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	value, _ := current.(string)
	return value
}

// entriesNames lists the entry names of one listing answer.
func entriesNames(t *testing.T, body map[string]any) []string {
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

// containsName reports whether one name is in the list.
func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
