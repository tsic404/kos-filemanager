// Package filetc5 checks FILE-TC-5: a text and an image preview show their
// content, a preview leaves the file's content and modification time
// unchanged, and a type this service cannot render is refused with a reason
// instead of a blank page or a download (FILE-AC-3, FILE-C-3).
package filetc5

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	volumeapi "github.com/tsic404/kosd/pkg/volume"
	"github.com/tsic404/kosd/pkg/write"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-filemanager/pkg/dataplane"
	"github.com/tsic404/kos-filemanager/tests/acceptance/fixtures"
)

// modified is the modification time the case writes on the previewed files, so
// the answer's modifiedAt is compared with an exact instant.
var modified = time.Date(2026, 4, 3, 10, 15, 0, 0, time.UTC)

// TestTC5PreviewShowsContentAndLeavesTheFileAlone walks one text file and one
// image: each preview shows the file's content, and the bytes and modification
// time on disk are exactly what they were before the preview (FILE-C-3).
func TestTC5PreviewShowsContentAndLeavesTheFileAlone(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "preview-data", fixtures.MemberOne, volumeapi.PhaseBound)
	root := system.VolumeDir(t, harness.Namespace, "preview-data")

	textContent := "第一行：预览不应改动文件\nsecond line\n"
	textPath := filepath.Join(root, "notes.txt")
	writeFile(t, textPath, []byte(textContent))
	setModified(t, textPath, modified)

	imagePath := filepath.Join(root, "photo.png")
	writeFile(t, imagePath, samplePNG(t))
	setModified(t, imagePath, modified)

	window := system.OpenWindow(t, fixtures.MemberOne)

	// Snapshot both files before any preview.
	textBefore := snapshot(t, textPath)
	imageBefore := snapshot(t, imagePath)

	status, body := system.DataPlane(t, http.MethodGet, previewPath(harness.Namespace, "preview-data", "notes.txt"),
		fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("preview notes.txt = %d %v, want 200", status, body)
	}
	if kind, _ := body["kind"].(string); kind != dataplane.PreviewText {
		t.Fatalf("notes.txt kind = %v, want %q", body["kind"], dataplane.PreviewText)
	}
	if content, _ := body["content"].(string); content != textContent {
		t.Fatalf("notes.txt content = %q, want the file's own text %q", content, textContent)
	}
	if contentType, _ := body["contentType"].(string); !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("notes.txt contentType = %q, want text/plain", contentType)
	}
	if size, _ := body["size"].(float64); int64(size) != int64(len(textContent)) {
		t.Fatalf("notes.txt size = %v, want %d", body["size"], len(textContent))
	}
	if got := modifiedAt(t, body); !got.Equal(modified) {
		t.Fatalf("notes.txt modifiedAt = %s, want %s", got, modified)
	}

	status, body = system.DataPlane(t, http.MethodGet, previewPath(harness.Namespace, "preview-data", "photo.png"),
		fixtures.Bearer(window.Token))
	if status != http.StatusOK {
		t.Fatalf("preview photo.png = %d %v, want 200", status, body)
	}
	if kind, _ := body["kind"].(string); kind != dataplane.PreviewImage {
		t.Fatalf("photo.png kind = %v, want %q", body["kind"], dataplane.PreviewImage)
	}
	if contentType, _ := body["contentType"].(string); contentType != "image/png" {
		t.Fatalf("photo.png contentType = %q, want image/png", contentType)
	}
	dataURL, _ := body["content"].(string)
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(dataURL, prefix) {
		t.Fatalf("photo.png content = %.32q, want a PNG data URL", dataURL)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURL, prefix))
	if err != nil {
		t.Fatalf("decode the image data URL: %v", err)
	}
	if !bytes.Equal(decoded, imageBefore.content) {
		t.Fatalf("photo.png preview carries %d bytes, want the file's own %d", len(decoded), len(imageBefore.content))
	}

	// Neither preview changed the files on disk.
	for name, before := range map[string]snapshotResult{"notes.txt": textBefore, "photo.png": imageBefore} {
		if after := snapshot(t, filepath.Join(root, name)); !after.equal(before) {
			t.Fatalf("%s changed after the preview: content %v, modification time %v",
				name, after.content, !after.modified.Equal(before.modified))
		}
	}
}

// TestTC5UnsupportedTypeIsRefusedWithAReason checks FILE-C-3's refusal: an
// unsupported type answers a reason and neither a blank body nor the file as a
// download, while a directory and a missing path each name their own reason.
func TestTC5UnsupportedTypeIsRefusedWithAReason(t *testing.T) {
	system := fixtures.Start(t)
	system.Account(t, fixtures.MemberOne, fixtures.MemberOnePass, write.RoleMember)
	system.CreateVolume(t, "preview-data", fixtures.MemberOne, volumeapi.PhaseBound)
	root := system.VolumeDir(t, harness.Namespace, "preview-data")

	archivePath := filepath.Join(root, "archive.zip")
	archiveContent := []byte("PK\x03\x04\x14\x00\x00\x00binary-body-not-for-preview")
	writeFile(t, archivePath, archiveContent)
	if err := os.Mkdir(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("create docs: %v", err)
	}

	// A symbolic link that points outside the volume is refused as a boundary,
	// so an unsupported type is not the only refusal a preview can answer.
	outside := t.TempDir()
	outsideContent := "leak-content\n"
	writeFile(t, filepath.Join(outside, "leak.txt"), []byte(outsideContent))
	if err := os.Symlink(filepath.Join(outside, "leak.txt"), filepath.Join(root, "outside.txt")); err != nil {
		t.Fatalf("link the outside file: %v", err)
	}

	window := system.OpenWindow(t, fixtures.MemberOne)

	status, headers, body := system.DataPlaneResponse(t, http.MethodGet,
		previewPath(harness.Namespace, "preview-data", "archive.zip"), fixtures.Bearer(window.Token))
	if status != http.StatusUnsupportedMediaType {
		t.Fatalf("preview archive.zip = %d %v, want 415", status, body)
	}
	if code := fixtures.ErrorCode(t, body); code != dataplane.CodePreviewUnsupported {
		t.Fatalf("archive.zip code = %q, want %q", code, dataplane.CodePreviewUnsupported)
	}
	reason := fixtures.ErrorMessage(t, body)
	if !strings.Contains(reason, ".zip") || !strings.Contains(reason, "仅支持") {
		t.Fatalf("archive.zip reason = %q, want it to name the type and what is supported", reason)
	}
	// No download, and no content smuggled into the answer.
	if disposition := headers.Get("Content-Disposition"); disposition != "" {
		t.Fatalf("archive.zip carries Content-Disposition %q, want no download", disposition)
	}
	if contentType := headers.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("archive.zip Content-Type = %q, want a JSON refusal", contentType)
	}
	if _, carried := body["content"]; carried {
		t.Fatalf("archive.zip refusal carries content: %v", body)
	}
	assertNoContent(t, body, string(archiveContent))

	status, body = system.DataPlane(t, http.MethodGet, previewPath(harness.Namespace, "preview-data", "docs"),
		fixtures.Bearer(window.Token))
	if status != http.StatusBadRequest {
		t.Fatalf("preview docs = %d %v, want 400", status, body)
	}
	if reason := fixtures.ErrorMessage(t, body); !strings.Contains(reason, "目录") {
		t.Fatalf("preview docs reason = %q, want it to name the directory", reason)
	}

	status, body = system.DataPlane(t, http.MethodGet, previewPath(harness.Namespace, "preview-data", "absent.txt"),
		fixtures.Bearer(window.Token))
	if status != http.StatusNotFound {
		t.Fatalf("preview absent.txt = %d %v, want 404", status, body)
	}
	if code := fixtures.ErrorCode(t, body); code != dataplane.CodePathNotFound {
		t.Fatalf("preview absent.txt code = %q, want %q", code, dataplane.CodePathNotFound)
	}
	if reason := fixtures.ErrorMessage(t, body); reason == "" {
		t.Fatalf("preview absent.txt carries no reason: %v", body)
	}

	status, body = system.DataPlane(t, http.MethodGet, previewPath(harness.Namespace, "preview-data", "outside.txt"),
		fixtures.Bearer(window.Token))
	if status != http.StatusForbidden {
		t.Fatalf("preview outside.txt = %d %v, want 403", status, body)
	}
	if code := fixtures.ErrorCode(t, body); code != dataplane.CodePathOutside {
		t.Fatalf("preview outside.txt code = %q, want %q", code, dataplane.CodePathOutside)
	}
	assertNoContent(t, body, outsideContent)
}

// snapshotResult is one file's content and modification time on disk.
type snapshotResult struct {
	content  []byte
	modified time.Time
}

// equal reports whether two snapshots are the same bytes at the same time.
func (s snapshotResult) equal(other snapshotResult) bool {
	return bytes.Equal(s.content, other.content) && s.modified.Equal(other.modified)
}

// snapshot reads one file's bytes and modification time.
func snapshot(t *testing.T, path string) snapshotResult {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return snapshotResult{content: content, modified: info.ModTime().UTC()}
}

// samplePNG returns a small valid PNG, so the image preview is exercised on a
// file a browser would render.
func samplePNG(t *testing.T) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatalf("encode the sample PNG: %v", err)
	}
	return encoded.Bytes()
}

// previewPath names one preview of one volume with the file path inside it.
func previewPath(namespace, name, inside string) string {
	return "/files/volumes/" + namespace + "/" + name + "/preview?path=" + url.QueryEscape(inside)
}

// modifiedAt reads one answer's modification time.
func modifiedAt(t *testing.T, body map[string]any) time.Time {
	t.Helper()
	raw, ok := body["modifiedAt"].(string)
	if !ok {
		t.Fatalf("answer carries no modification time: %v", body)
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("parse modifiedAt %q: %v", raw, err)
	}
	return parsed
}

// assertNoContent checks that one answer carries none of the given content.
func assertNoContent(t *testing.T, body map[string]any, content string) {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode the answer: %v", err)
	}
	if strings.Contains(string(encoded), strings.TrimSpace(content)) {
		t.Fatalf("answer carries content it must not serve: %s", encoded)
	}
}

// writeFile writes one file into the served directory.
func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o644); err != nil {
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
