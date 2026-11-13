package dataplane

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSettleStagingStatesWhatStayed checks the two answers a failed copy can
// give: a cleared staging tree claims no half result, and a tree the destination
// would not let go is named with the entry the volume still holds. A request
// cannot drive the second answer — the volume would have to stop removing
// entries mid-request — so the answer is checked where it is built.
func TestSettleStagingStatesWhatStayed(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "target")
	staged := filepath.Join(parent, ".kos-partial-top-probe", "keeper")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatalf("create the staged tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staged, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write into the staged tree: %v", err)
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		t.Fatalf("open the destination root: %v", err)
	}
	defer root.Close()
	cause := &refusal{
		status:  http.StatusInternalServerError,
		code:    CodeFilesUnreadable,
		message: "读取卷内路径 top/zfail/bad.txt 失败",
	}

	// A destination directory that refuses the removal keeps the tree, so the
	// answer names it instead of claiming nothing was left.
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatalf("make the destination directory read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	kept := settleStaging(root, ".kos-partial-top-probe", cause)
	if strings.Contains(kept.message, "未留下半份结果") {
		t.Fatalf("answer claims no half result while the tree stayed: %q", kept.message)
	}
	if !strings.Contains(kept.message, ".kos-partial-top-probe") {
		t.Fatalf("answer does not name the staging entry it left: %q", kept.message)
	}
	if _, err := os.Lstat(filepath.Join(parent, ".kos-partial-top-probe")); err != nil {
		t.Fatalf("the destination holds no staging entry to report: %v", err)
	}

	// Once the directory lets the tree go, the answer states that nothing was
	// left and keeps the failure it reports.
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatalf("restore the destination directory: %v", err)
	}
	cleared := settleStaging(root, ".kos-partial-top-probe", cause)
	if !strings.Contains(cleared.message, "未留下半份结果") {
		t.Fatalf("answer does not state the staging tree was cleared: %q", cleared.message)
	}
	if _, err := os.Lstat(filepath.Join(parent, ".kos-partial-top-probe")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the staging entry stayed after the cleared answer: %v", err)
	}
	if cleared.status != cause.status || cleared.code != cause.code || !strings.HasPrefix(cleared.message, cause.message) {
		t.Fatalf("cleared answer = %+v, want the failure it reports kept", cleared)
	}
}
