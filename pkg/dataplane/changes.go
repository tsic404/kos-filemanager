package dataplane

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Refusals of the change surface (FILE-C-5). A change is refused when its
// target name is taken, when a delete has not been confirmed yet, and when it
// would remove the volume root; a filesystem failure is answered with its
// reason instead of leaving a half result behind.
const (
	// CodeTargetExists refuses a change whose target name the destination
	// already holds, instead of replacing it.
	CodeTargetExists = "target_exists"
	// CodeDeleteUnconfirmed answers a delete with what it would remove; the
	// request that carries the confirmation performs it.
	CodeDeleteUnconfirmed = "delete_confirmation_required"
	// CodeVolumeRootProtected refuses a change that would remove the volume
	// root itself.
	CodeVolumeRootProtected = "volume_root_protected"
	// CodeFilesWriteFailed refuses a change the filesystem failed.
	CodeFilesWriteFailed = "files_write_failed"
	// CodePathNotRemovable refuses a change the host could only carry out in
	// part, because a directory it would empty is not writable. Nothing is
	// removed (FILE-C-5).
	CodePathNotRemovable = "path_not_removable"
	// CodeDeleteIncomplete reports a deletion that removed part of an entry
	// before the filesystem failed; the answer names what is left.
	CodeDeleteIncomplete = "delete_incomplete"
	// CodeMoveIncomplete reports a move whose copy landed completely while the
	// source could not be emptied; the answer names what is left.
	CodeMoveIncomplete = "move_incomplete"
)

// maxChangeBody bounds one change request. The bodies name paths and a
// confirmation flag, so the bound is generous and still refuses a runaway one.
const maxChangeBody = 64 << 10

// Change is the outcome of one change operation (FILE-C-5): the operation that
// ran, the source it started from, and where the result now is.
type Change struct {
	// Operation is rename, copy, move or delete.
	Operation string `json:"operation"`
	// Volume is the namespace/name reference of the source volume.
	Volume string `json:"volume"`
	// Path is the source path inside the volume.
	Path string `json:"path"`
	// TargetVolume is the namespace/name reference the result landed in.
	TargetVolume string `json:"targetVolume,omitempty"`
	// TargetPath is the path the result landed at.
	TargetPath string `json:"targetPath,omitempty"`
	// Deleted marks a delete that ran.
	Deleted bool `json:"deleted,omitempty"`
	// ObservedAt is when this service performed the change (KOSD-C-13).
	ObservedAt time.Time `json:"observedAt"`
}

// DeletionPreview is what one unconfirmed delete would remove (FILE-C-5): the
// entries directly inside the entry, and the totals of the whole subtree the
// confirmation states.
type DeletionPreview struct {
	// Volume is the namespace/name reference of the volume.
	Volume string `json:"volume"`
	// Path is the path the deletion would remove.
	Path string `json:"path"`
	// EntryCount is how many entries the deletion removes.
	EntryCount int `json:"entryCount"`
	// SubdirectoryCount is how many of those entries are directories.
	SubdirectoryCount int `json:"subdirectoryCount"`
	// Entries are the entry names directly inside the path, in name order.
	Entries []string `json:"entries"`
	// Truncated marks Entries cut to MaxDirectoryEntries.
	Truncated bool `json:"truncated"`
}

// renameRequest names one entry and the name it takes in its own directory.
type renameRequest struct {
	// Path is the entry's current path inside the volume.
	Path string `json:"path"`
	// Name is the new base name it takes in the same directory.
	Name string `json:"name"`
}

// changeTransferRequest names one entry and where a copy or move puts it.
type changeTransferRequest struct {
	// Path is the entry's current path inside the source volume.
	Path string `json:"path"`
	// Target is the volume and path the operation writes to.
	Target transferAddress `json:"target"`
}

// transferAddress is one volume and path a copy or move writes to. A volume the
// request does not name is the source volume.
type transferAddress struct {
	// Namespace is the target volume's namespace.
	Namespace string `json:"namespace"`
	// Name is the target volume's name.
	Name string `json:"name"`
	// Path is the target path inside that volume.
	Path string `json:"path"`
}

// deleteRequest names one entry and whether the reader confirmed its removal.
type deleteRequest struct {
	// Path is the entry's path inside the volume.
	Path string `json:"path"`
	// Confirm states that the reader saw what the deletion removes.
	Confirm bool `json:"confirm"`
}

// handleRename renames one entry inside its own directory (FILE-C-5). A name the
// directory already holds is refused with its reason: the entry it names is
// never replaced.
func (s *Service) handleRename(w http.ResponseWriter, r *http.Request) {
	binding := bindingOf(r.Context())
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	var request renameRequest
	if !decodeChange(w, r, &request) {
		return
	}
	source, ok := cleanPath(request.Path)
	if !ok {
		invalidPath(w)
		return
	}
	if source == "" {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "卷根不能重命名"}).write(w)
		return
	}
	newName, ok := cleanName(request.Name)
	if !ok {
		(&refusal{http.StatusBadRequest, CodePathInvalid,
			"名称无效：不能为空，也不能包含目录分隔符或上级目录"}).write(w)
		return
	}
	target := joinRelative(directoryOf(source), newName)
	opened, rejected := s.openVolumeRoot(r.Context(), binding, namespace, name)
	if rejected != nil {
		rejected.write(w)
		return
	}
	defer opened.Close()
	address, rejected := opened.address(source)
	if rejected != nil {
		rejected.write(w)
		return
	}
	volume := volumeLabel(namespace, name)
	if rejected := targetFree(opened.root, target, volume+":"+target); rejected != nil {
		rejected.write(w)
		return
	}
	if err := opened.root.Rename(address, target); err != nil {
		s.log.Warn("rename an entry", "volume", volume, "path", source, "error", err)
		(&refusal{http.StatusInternalServerError, CodeFilesWriteFailed,
			"重命名 " + source + " 失败，源与目标都保持原样"}).write(w)
		return
	}
	writeJSON(w, http.StatusOK, Change{
		Operation:    "rename",
		Volume:       volume,
		Path:         source,
		TargetVolume: volume,
		TargetPath:   target,
		ObservedAt:   s.now().UTC(),
	})
}

// handleCopy copies one entry into an authorized volume (FILE-C-5).
func (s *Service) handleCopy(w http.ResponseWriter, r *http.Request) {
	s.transfer(w, r, "copy")
}

// handleMove moves one entry into an authorized volume (FILE-C-5).
func (s *Service) handleMove(w http.ResponseWriter, r *http.Request) {
	s.transfer(w, r, "move")
}

// transfer performs one copy or move. Both ends are authorized volumes: the
// source is the request's own volume, and the target is opened the same way, so
// another account's volume refuses the change instead of receiving it
// (FILE-C-1, FILE-C-5). A move within one volume is a rename; a move across
// volumes lands the copy and only then removes the source.
func (s *Service) transfer(w http.ResponseWriter, r *http.Request, operation string) {
	binding := bindingOf(r.Context())
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	var request changeTransferRequest
	if !decodeChange(w, r, &request) {
		return
	}
	source, ok := cleanPath(request.Path)
	if !ok {
		invalidPath(w)
		return
	}
	if source == "" {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "卷根不能" + describeOperation(operation)}).write(w)
		return
	}
	targetNamespace := request.Target.Namespace
	if targetNamespace == "" {
		targetNamespace = namespace
	}
	targetName := request.Target.Name
	if targetName == "" {
		targetName = name
	}
	target, ok := cleanPath(request.Target.Path)
	if !ok {
		invalidPath(w)
		return
	}
	if target == "" {
		(&refusal{http.StatusBadRequest, CodePathInvalid,
			"目标是卷根，不能作为" + describeOperation(operation) + "的落点"}).write(w)
		return
	}
	opened, rejected := s.openVolumeRoot(r.Context(), binding, namespace, name)
	if rejected != nil {
		rejected.write(w)
		return
	}
	defer opened.Close()
	address, rejected := opened.address(source)
	if rejected != nil {
		rejected.write(w)
		return
	}
	volume := volumeLabel(namespace, name)
	targetVolume := volumeLabel(targetNamespace, targetName)
	label := targetVolume + ":" + target
	same := sameVolume(namespace, name, targetNamespace, targetName)
	destination := opened
	if !same {
		if destination, rejected = s.openVolumeRoot(r.Context(), binding, targetNamespace, targetName); rejected != nil {
			rejected.write(w)
			return
		}
		defer destination.Close()
	}
	info, err := opened.root.Stat(address)
	if err != nil {
		readFailure(source, err).write(w)
		return
	}
	if same && info.IsDir() && strings.HasPrefix(target, source+"/") {
		(&refusal{http.StatusBadRequest, CodePathInvalid,
			"目标 " + target + " 在源 " + source + " 内，不能作为" + describeOperation(operation) + "的落点"}).write(w)
		return
	}
	if rejected := targetParent(destination.root, target, label); rejected != nil {
		rejected.write(w)
		return
	}
	if rejected := targetFree(destination.root, target, label); rejected != nil {
		rejected.write(w)
		return
	}
	if same && operation == "move" {
		// Inside one volume the move is a rename: it either happens or leaves
		// both names as they were.
		if err := opened.root.Rename(address, target); err != nil {
			s.log.Warn("move an entry inside one volume", "volume", label, "path", source, "error", err)
			(&refusal{http.StatusInternalServerError, CodeFilesWriteFailed,
				"移动 " + source + " 失败，源与目标都保持原样"}).write(w)
			return
		}
	} else {
		if operation == "move" {
			// A move empties the source, so it does not start when part of the
			// source cannot be removed: the refusal touches nothing (FILE-C-5).
			if rejected := sourceEmptiable(opened, source); rejected != nil {
				rejected.write(w)
				return
			}
		}
		if rejected := copyInto(opened, address, destination, target); rejected != nil {
			rejected.write(w)
			return
		}
		if operation == "move" {
			if err := opened.root.RemoveAll(address); err != nil {
				// The copy already landed, so it is kept: the source keeps what
				// could not be removed and the answer says so instead of
				// claiming a state neither end is in (FILE-C-5).
				remaining, counted := residualEntries(opened.root, source)
				s.log.Warn("remove the source of a move", "volume", volume, "path", source, "error", err)
				(&refusal{http.StatusInternalServerError, CodeMoveIncomplete,
					moveIncompleteMessage(source, targetVolume, target, remaining, counted)}).write(w)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, Change{
		Operation:    operation,
		Volume:       volume,
		Path:         source,
		TargetVolume: targetVolume,
		TargetPath:   target,
		ObservedAt:   s.now().UTC(),
	})
}

// handleDelete removes one entry after its confirmation (FILE-C-5). A request
// without the confirmation answers what the deletion would remove and removes
// nothing; the volume root is never deletable.
func (s *Service) handleDelete(w http.ResponseWriter, r *http.Request) {
	binding := bindingOf(r.Context())
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	var request deleteRequest
	if !decodeChange(w, r, &request) {
		return
	}
	relative, ok := cleanPath(request.Path)
	if !ok {
		invalidPath(w)
		return
	}
	if relative == "" {
		(&refusal{http.StatusBadRequest, CodeVolumeRootProtected, "卷根不可删除"}).write(w)
		return
	}
	opened, rejected := s.openVolumeRoot(r.Context(), binding, namespace, name)
	if rejected != nil {
		rejected.write(w)
		return
	}
	defer opened.Close()
	address, rejected := opened.address(relative)
	if rejected != nil {
		rejected.write(w)
		return
	}
	volume := volumeLabel(namespace, name)
	if !request.Confirm {
		preview, isDirectory, rejected := previewDeletion(opened.root, volume, relative)
		if rejected != nil {
			rejected.write(w)
			return
		}
		writeBody(w, http.StatusConflict, map[string]any{
			"error": map[string]any{
				"code":    CodeDeleteUnconfirmed,
				"message": deletionMessage(relative, preview, isDirectory),
			},
			"confirmation": preview,
		})
		return
	}
	// A deletion that could only be carried out in part does not start: the
	// refusal reports the directory that blocks it and removes nothing
	// (FILE-C-5).
	blocking, rejected := unwritableDirectory(opened.root, opened, relative)
	if rejected != nil {
		rejected.write(w)
		return
	}
	if blocking != "" {
		(&refusal{http.StatusConflict, CodePathNotRemovable,
			"删除 " + relative + " 未执行：目录 " + blocking + " 不可写；未删除任何内容"}).write(w)
		return
	}
	if err := opened.root.RemoveAll(address); err != nil {
		s.log.Warn("delete an entry", "volume", volume, "path", relative, "error", err)
		remaining, counted := residualEntries(opened.root, relative)
		(&refusal{http.StatusInternalServerError, CodeDeleteIncomplete,
			deleteIncompleteMessage(relative, remaining, counted)}).write(w)
		return
	}
	writeJSON(w, http.StatusOK, Change{
		Operation:  "delete",
		Volume:     volume,
		Path:       relative,
		Deleted:    true,
		ObservedAt: s.now().UTC(),
	})
}

// previewDeletion counts what removing one entry would take with it, and lists
// the entries directly inside it (FILE-C-5). The second answer reports whether
// the entry is a directory, which the confirmation's wording names.
func previewDeletion(root *os.Root, volume, relative string) (DeletionPreview, bool, *refusal) {
	preview := DeletionPreview{Volume: volume, Path: relative, Entries: []string{}}
	info, err := root.Lstat(relative)
	if err != nil {
		return preview, false, readFailure(relative, err)
	}
	if !info.IsDir() {
		preview.EntryCount = 1
		preview.Entries = []string{path.Base(relative)}
		return preview, false, nil
	}
	children, rejected := readEntries(root, relative)
	if rejected != nil {
		return preview, true, rejected
	}
	for _, child := range children {
		preview.Entries = append(preview.Entries, child.Name())
	}
	if len(preview.Entries) > MaxDirectoryEntries {
		preview.Entries = preview.Entries[:MaxDirectoryEntries]
		preview.Truncated = true
	}
	preview.EntryCount += len(children)
	for _, child := range children {
		if !child.IsDir() {
			continue
		}
		preview.SubdirectoryCount++
		if rejected := countDeletion(root, joinRelative(relative, child.Name()), &preview); rejected != nil {
			return preview, true, rejected
		}
	}
	return preview, true, nil
}

// countDeletion adds one directory's subtree to the preview's totals without
// following a symbolic link: a link is one entry, not the tree it names.
func countDeletion(root *os.Root, relative string, preview *DeletionPreview) *refusal {
	children, rejected := readEntries(root, relative)
	if rejected != nil {
		return rejected
	}
	preview.EntryCount += len(children)
	for _, child := range children {
		if !child.IsDir() {
			continue
		}
		preview.SubdirectoryCount++
		if rejected := countDeletion(root, joinRelative(relative, child.Name()), preview); rejected != nil {
			return rejected
		}
	}
	return nil
}

// readEntries reads one directory's entries below the volume root, in name
// order: a change reports the same path and clears the same staging tree on
// every filesystem instead of following the directory's own order.
func readEntries(root *os.Root, relative string) ([]fs.DirEntry, *refusal) {
	directory, err := root.Open(insidePath(relative))
	if err != nil {
		return nil, readFailure(relative, err)
	}
	defer directory.Close()
	children, err := directory.ReadDir(-1)
	if err != nil {
		return nil, readFailure(relative, err)
	}
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	return children, nil
}

// readFailure answers one read the filesystem failed, naming the path.
func readFailure(relative string, err error) *refusal {
	if errors.Is(err, fs.ErrNotExist) {
		return &refusal{http.StatusNotFound, CodePathNotFound, "路径 " + relative + " 不存在"}
	}
	return &refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内路径 " + relative + " 失败"}
}

// deletionMessage states what one unconfirmed delete would remove.
func deletionMessage(relative string, preview DeletionPreview, isDirectory bool) string {
	kind := "文件"
	if isDirectory {
		kind = "目录"
	}
	message := "删除" + kind + " " + relative + " 需要确认：将删除 " + strconv.Itoa(preview.EntryCount) + " 个条目"
	if preview.SubdirectoryCount > 0 {
		message += "（含 " + strconv.Itoa(preview.SubdirectoryCount) + " 个子目录）"
	}
	return message + "，确认后生效"
}

// copyInto reproduces one source entry at one destination path. The copy is
// built at a staging entry beside the target and renamed into place, so a
// failure removes the staging entry and the target never shows a half result
// (FILE-C-5).
func copyInto(source *volumeRoot, from string, destination *volumeRoot, to string) *refusal {
	staged := joinRelative(directoryOf(to), stagingEntry(to))
	if rejected := copyEntry(source, from, destination, staged); rejected != nil {
		return settleStaging(destination.root, staged, rejected)
	}
	if err := destination.root.Rename(staged, to); err != nil {
		return settleStaging(destination.root, staged,
			&refusal{http.StatusInternalServerError, CodeFilesWriteFailed, "写入目标 " + to + " 失败"})
	}
	return nil
}

// settleStaging removes one failed copy's staging tree and states what the
// destination holds: a cleared tree means no half result, one that could not be
// cleared is named instead of being claimed away (FILE-C-5).
func settleStaging(destination *os.Root, staged string, cause *refusal) *refusal {
	if err := removeStaged(destination, staged); err != nil {
		return &refusal{cause.status, cause.code,
			cause.message + "；目标卷残留暂存条目 " + staged + " 未清除"}
	}
	return &refusal{cause.status, cause.code, cause.message + "，未留下半份结果"}
}

// removeStaged removes one failed copy's staging tree. A copy gives each
// directory the source's mode once its content is in place, so the tree can
// hold read-only directories a plain removal could not empty; every directory
// is restored to owner-writable first (FILE-C-5).
func removeStaged(destination *os.Root, staged string) error {
	if err := writableTree(destination, staged); err != nil {
		return err
	}
	return destination.RemoveAll(staged)
}

// writableTree gives every directory of one staged tree the write permission
// its removal needs. A symbolic link is one entry and is not followed.
func writableTree(root *os.Root, relative string) error {
	info, err := root.Lstat(relative)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	if !info.IsDir() {
		return nil
	}
	if err := root.Chmod(relative, info.Mode().Perm()|0o700); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	directory, err := root.Open(insidePath(relative))
	if err != nil {
		return err
	}
	children, readErr := directory.ReadDir(-1)
	if err := errors.Join(readErr, directory.Close()); err != nil {
		return err
	}
	for _, child := range children {
		if err := writableTree(root, joinRelative(relative, child.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyEntry reproduces one entry, recursively for a directory. The caller owns
// the staging entry's cleanup, so a failure deep inside a tree is removed in one
// place.
func copyEntry(source *volumeRoot, from string, destination *volumeRoot, to string) *refusal {
	info, err := source.root.Lstat(from)
	if err != nil {
		return readFailure(from, err)
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return copyLink(source, from, destination, to)
	case info.IsDir():
		return copyDirectory(source, from, destination, to, info.Mode().Perm())
	case info.Mode().IsRegular():
		return copyFile(source, from, destination, to, info.Mode().Perm())
	default:
		return &refusal{http.StatusBadRequest, CodePathInvalid,
			"源 " + from + " 不是普通文件或目录，不能复制或移动"}
	}
}

// copyFile copies one regular file's content and permission bits.
func copyFile(source *volumeRoot, from string, destination *volumeRoot, to string, perm fs.FileMode) *refusal {
	in, err := source.root.Open(from)
	if err != nil {
		return readFailure(from, err)
	}
	defer in.Close()
	out, err := destination.root.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return changeWriteFailure(to, err)
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		return changeWriteFailure(to, errors.Join(copyErr, closeErr))
	}
	return nil
}

// copyDirectory recreates one directory and its content below the staging entry.
// The new directory is writable while it is filled and takes the source's mode
// once its content is in place, so a read-only directory is still copyable.
func copyDirectory(source *volumeRoot, from string, destination *volumeRoot, to string, perm fs.FileMode) *refusal {
	if err := destination.root.Mkdir(to, perm|0o700); err != nil {
		return changeWriteFailure(to, err)
	}
	children, rejected := readEntries(source.root, from)
	if rejected != nil {
		return rejected
	}
	for _, child := range children {
		if rejected := copyEntry(source, joinRelative(from, child.Name()), destination, joinRelative(to, child.Name())); rejected != nil {
			return rejected
		}
	}
	if err := destination.root.Chmod(to, perm); err != nil {
		return changeWriteFailure(to, err)
	}
	return nil
}

// copyLink recreates one symbolic link; the link itself is copied, not the path
// it names.
func copyLink(source *volumeRoot, from string, destination *volumeRoot, to string) *refusal {
	target, err := source.root.Readlink(from)
	if err != nil {
		return readFailure(from, err)
	}
	if err := destination.root.Symlink(target, to); err != nil {
		return changeWriteFailure(to, err)
	}
	return nil
}

// changeWriteFailure answers one write the filesystem failed, naming the path.
// The copy states afterwards whether anything stayed behind, so this reason
// claims nothing about the staging tree it has not cleared yet.
func changeWriteFailure(relative string, err error) *refusal {
	if errors.Is(err, fs.ErrPermission) {
		return &refusal{http.StatusForbidden, CodeFilesWriteFailed,
			"目标 " + relative + " 不可写"}
	}
	return &refusal{http.StatusInternalServerError, CodeFilesWriteFailed,
		"写入目标 " + relative + " 失败"}
}

// stagingEntry names one temporary entry beside a change's final target, so a
// copy can be completed before it takes the target name.
func stagingEntry(entry string) string {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		// A nonce the system cannot randomise falls back to the clock; the
		// exclusive create still refuses a name that is already taken.
		binary.LittleEndian.PutUint64(nonce, uint64(time.Now().UnixNano()))
	}
	base := path.Base(entry)
	if len(base) > 24 {
		base = base[:24]
	}
	return ".kos-partial-" + base + "-" + hex.EncodeToString(nonce)
}

// targetFree reports whether one target name is still free. A name the
// destination already holds is refused with its reason rather than replaced
// (FILE-C-5).
func targetFree(root *os.Root, target, label string) *refusal {
	_, err := root.Lstat(target)
	switch {
	case err == nil:
		return &refusal{http.StatusConflict, CodeTargetExists, "目标 " + label + " 已存在，未覆盖"}
	case errors.Is(err, fs.ErrNotExist):
		return nil
	default:
		return &refusal{http.StatusInternalServerError, CodeFilesWriteFailed, "读取目标 " + label + " 失败"}
	}
}

// targetParent checks that one target path's directory exists in the
// destination volume: a change never creates the directory it lands in.
func targetParent(root *os.Root, target, label string) *refusal {
	parent := directoryOf(target)
	info, err := root.Stat(insidePath(parent))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &refusal{http.StatusNotFound, CodePathNotFound, "目标目录 " + parent + " 不存在"}
		}
		return &refusal{http.StatusInternalServerError, CodeFilesWriteFailed, "读取目标目录 " + parent + " 失败"}
	}
	if !info.IsDir() {
		return &refusal{http.StatusBadRequest, CodePathInvalid, "目标目录 " + parent + " 不是目录"}
	}
	return nil
}

// sourceEmptiable refuses one move the host could only carry out in part. Every
// directory the move would empty is checked before the copy lands, so the
// refusal leaves the source whole and the target untouched (FILE-C-5).
func sourceEmptiable(opened *volumeRoot, source string) *refusal {
	blocking, rejected := unwritableDirectory(opened.root, opened, source)
	if rejected != nil {
		return rejected
	}
	if blocking == "" {
		return nil
	}
	return &refusal{http.StatusConflict, CodePathNotRemovable,
		"移动 " + source + " 未执行：目录 " + blocking + " 不可写，移动会留下部分条目；源与目标都未改动"}
}

// unwritableDirectory names the first directory one removal would have to empty
// that the process cannot write: the entry's own directory, the entry itself
// when it is a directory, and every directory below it. An empty name means the
// removal can go ahead.
func unwritableDirectory(root *os.Root, opened *volumeRoot, relative string) (string, *refusal) {
	parent := directoryOf(relative)
	writable, rejected := directoryWritable(opened.resolved, parent)
	if rejected != nil || !writable {
		return parent, rejected
	}
	info, err := root.Lstat(relative)
	if err != nil {
		return "", readFailure(relative, err)
	}
	if !info.IsDir() {
		return "", nil
	}
	return unwritableBelow(root, opened, relative)
}

// unwritableBelow walks one directory and reports the first directory below or
// equal to it the process cannot write. A symbolic link is not descended into:
// removing it unlinks the link, not the tree it names.
func unwritableBelow(root *os.Root, opened *volumeRoot, relative string) (string, *refusal) {
	writable, rejected := directoryWritable(opened.resolved, relative)
	if rejected != nil || !writable {
		return relative, rejected
	}
	children, rejected := readEntries(root, relative)
	if rejected != nil {
		return "", rejected
	}
	for _, child := range children {
		if !child.IsDir() {
			continue
		}
		blocking, rejected := unwritableBelow(root, opened, joinRelative(relative, child.Name()))
		if rejected != nil || blocking != "" {
			return blocking, rejected
		}
	}
	return "", nil
}

// directoryWritable reports whether the process may remove entries from one
// directory of a volume. The check is a read-only access test, so a refusal
// leaves the volume exactly as it was (FILE-C-5).
func directoryWritable(resolved, relative string) (bool, *refusal) {
	target := resolved
	if relative != "" {
		target = filepath.Join(resolved, filepath.FromSlash(relative))
	}
	err := unix.Access(target, unix.W_OK)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM), errors.Is(err, unix.EROFS):
		return false, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, &refusal{http.StatusNotFound, CodePathNotFound, "路径 " + relative + " 不存在"}
	default:
		return false, &refusal{http.StatusInternalServerError, CodeFilesWriteFailed,
			"检查目录 " + relative + " 的写权限失败"}
	}
}

// residualEntries counts what one failed removal left behind, so the answer
// states the state the volume is actually in. The second result is false when
// the leftover could not be counted.
func residualEntries(root *os.Root, relative string) (int, bool) {
	info, err := root.Lstat(relative)
	if err != nil {
		return 0, errors.Is(err, fs.ErrNotExist)
	}
	if !info.IsDir() {
		return 1, true
	}
	preview := DeletionPreview{}
	if rejected := countDeletion(root, relative, &preview); rejected != nil {
		return 0, false
	}
	return preview.EntryCount, true
}

// deleteIncompleteMessage states what a failed removal left behind.
func deleteIncompleteMessage(relative string, remaining int, counted bool) string {
	if !counted {
		return "删除 " + relative + " 未完成：部分条目已被删除，剩余条目无法统计"
	}
	return "删除 " + relative + " 未完成：部分条目已被删除，原处仍有 " + strconv.Itoa(remaining) + " 个条目"
}

// moveIncompleteMessage states that a move's copy is complete while its source
// could not be emptied (FILE-C-5).
func moveIncompleteMessage(source, targetVolume, target string, remaining int, counted bool) string {
	message := "移动 " + source + " 未完成：副本已完整落在 " + targetVolume + ":" + target
	if !counted {
		return message + "，源未能完全删除"
	}
	return message + "，源仍有 " + strconv.Itoa(remaining) + " 个条目未删除"
}

// decodeChange reads one change request's body. A body this service cannot read
// is refused instead of being treated as an empty request.
func decodeChange(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChangeBody)).Decode(target); err != nil {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "请求内容无效：需要一个 JSON 对象"}).write(w)
		return false
	}
	return true
}

// invalidPath answers one path this service will not address.
func invalidPath(w http.ResponseWriter) {
	(&refusal{http.StatusBadRequest, CodePathInvalid, "路径无效：不能是绝对路径或包含上级目录"}).write(w)
}

// cleanName turns one new entry name into a name a directory may hold. A name
// with a separator or a parent step addresses another directory, so it is
// refused instead of being normalised.
func cleanName(raw string) (string, bool) {
	if raw == "" || raw == "." || raw == ".." {
		return "", false
	}
	if strings.ContainsAny(raw, `/\`) || strings.ContainsRune(raw, 0) {
		return "", false
	}
	return raw, true
}

// directoryOf returns the directory part of one clean relative path.
func directoryOf(relative string) string {
	index := strings.LastIndexByte(relative, '/')
	if index < 0 {
		return ""
	}
	return relative[:index]
}

// joinRelative appends one entry name to a directory path, addressing the volume
// root as the empty path.
func joinRelative(directory, name string) string {
	if directory == "" {
		return name
	}
	return directory + "/" + name
}

// sameVolume reports whether two volume references name the same volume.
func sameVolume(namespace, name, otherNamespace, otherName string) bool {
	return namespace == otherNamespace && name == otherName
}

// volumeLabel renders one volume reference the way the answers name it.
func volumeLabel(namespace, name string) string { return namespace + "/" + name }

// describeOperation names one operation in a readable reason.
func describeOperation(operation string) string {
	if operation == "copy" {
		return "复制"
	}
	return "移动"
}
