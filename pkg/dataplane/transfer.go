package dataplane

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StagingDir is the volume-root directory in-flight uploads are written to.
// A rename inside that volume puts the finished bytes at the target name in one
// step, and the directory itself is never browsed (FILE-C-4).
const StagingDir = ".kos-transfers"

// Bounds this surface sets for itself: the opening document of one session and
// the sessions one service keeps before it drops the settled ones.
const (
	// MaxTransferRequestBytes bounds the session-opening request document.
	MaxTransferRequestBytes = 64 << 10
	// MaxTransfers bounds the sessions kept in memory; settled sessions are
	// dropped oldest first once the bound is reached.
	MaxTransfers = 512
	// MaxNameLength bounds one uploaded file name.
	MaxNameLength = 255
)

// States one transfer moves through: a conflict waits for confirmation, a
// completed transfer's bytes are at the target name, a cancelled or failed one
// leaves the target as it was (FILE-C-4).
const (
	// TransferStateOpen accepts content.
	TransferStateOpen = "open"
	// TransferStateConflict waits for the window to confirm replacing a file.
	TransferStateConflict = "conflict"
	// TransferStateCompleted has the whole content at the target name.
	TransferStateCompleted = "completed"
	// TransferStateCancelled was stopped before the content was whole.
	TransferStateCancelled = "cancelled"
	// TransferStateFailed stopped on an error before the content was whole.
	TransferStateFailed = "failed"
)

// Refusals of the transfer surface (FILE-C-4).
const (
	// CodeTransferNotFound refuses a session this service does not hold.
	CodeTransferNotFound = "transfer_not_found"
	// CodeTransferInvalid refuses a request that cannot start a session.
	CodeTransferInvalid = "transfer_invalid"
	// CodeUploadConflict refuses content for a session waiting for the
	// window's confirmation that the same name may be replaced.
	CodeUploadConflict = "upload_conflict"
	// CodeTransferClosed refuses an operation a settled session no longer
	// takes, such as content after cancelling.
	CodeTransferClosed = "transfer_closed"
	// CodeTransferSizeMismatch refuses content beyond the declared size and
	// fails the session rather than writing a different file.
	CodeTransferSizeMismatch = "transfer_size_mismatch"
	// CodePathReserved refuses a path into this service's staging area.
	CodePathReserved = "path_reserved"
	// CodeFileUnreadable refuses a download the filesystem will not read.
	CodeFileUnreadable = "file_unreadable"
	// CodeDownloadUnsupported refuses a download of anything but a regular
	// file, with the reason instead of an empty stream.
	CodeDownloadUnsupported = "download_unsupported"
)

// TransferConflict describes the file already at the target name when a
// session opened (FILE-C-4).
type TransferConflict struct {
	// Size is the existing file's size in bytes.
	Size int64 `json:"size"`
	// ModifiedAt is the existing file's modification time.
	ModifiedAt time.Time `json:"modifiedAt"`
}

// Transfer is one upload session: what it will write, how much of it has
// arrived, and the state the window shows while it runs (FILE-C-4).
type Transfer struct {
	// ID is the session identity.
	ID string `json:"id"`
	// Direction is upload; downloads are streamed without a session.
	Direction string `json:"direction"`
	// Volume is the namespace/name reference of the volume.
	Volume string `json:"volume"`
	// Path is the browsed directory the content is written into.
	Path string `json:"path"`
	// Name is the file name at the target.
	Name string `json:"name"`
	// Target is the path inside the volume the content is written to.
	Target string `json:"target"`
	// State is open, conflict, completed, cancelled or failed.
	State string `json:"state"`
	// Size is the declared content size, and the total progress ends at.
	Size int64 `json:"size"`
	// Received is how many bytes this session has accepted.
	Received int64 `json:"received"`
	// Overwrite reports whether the window confirmed replacing a same-name file.
	Overwrite bool `json:"overwrite"`
	// Conflict describes the file already at the target when the session opened.
	Conflict *TransferConflict `json:"conflict,omitempty"`
	// Reason explains a conflict or a failure in words the window shows.
	Reason string `json:"reason,omitempty"`
	// CreatedAt is when the session opened.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when the session last moved.
	UpdatedAt time.Time `json:"updatedAt"`
	// CompletedAt is when the whole content reached the target name.
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

// transfer is one session plus the facts this service keeps about it.
type transfer struct {
	Transfer
	// namespace and volumeName place the session's volume in the cluster.
	namespace, volumeName string
	// staging is the in-flight file's path inside the volume.
	staging string
	// own guards this session against concurrent requests.
	own sync.Mutex
}

// state reads one session's state without racing a request.
func (t *transfer) state() string {
	t.own.Lock()
	defer t.own.Unlock()
	return t.State
}

// snapshot copies one session for an answer.
func (t *transfer) snapshot() Transfer {
	t.own.Lock()
	defer t.own.Unlock()
	return t.copyLocked()
}

// copyLocked copies one session for an answer; the caller holds the lock.
func (t *transfer) copyLocked() Transfer {
	copied := t.Transfer
	if t.Transfer.Conflict != nil {
		conflict := *t.Transfer.Conflict
		copied.Conflict = &conflict
	}
	if t.Transfer.CompletedAt != nil {
		completed := *t.Transfer.CompletedAt
		copied.CompletedAt = &completed
	}
	return copied
}

// handleTransferOpen opens one upload session (FILE-C-4). The browsed
// directory and the file name are fixed here; a same-name file is reported
// instead of replaced until the window opens a session that confirms it.
func (s *Service) handleTransferOpen(w http.ResponseWriter, r *http.Request) {
	binding := bindingOf(r.Context())
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	opened, rejected := s.openVolumeRoot(r.Context(), binding, namespace, name)
	if rejected != nil {
		rejected.write(w)
		return
	}
	defer opened.Close()
	request, rejected := decodeTransferRequest(w, r)
	if rejected != nil {
		rejected.write(w)
		return
	}
	directory, ok := cleanPath(request.Path)
	if !ok {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "路径无效：不能是绝对路径或包含上级目录"}).write(w)
		return
	}
	if reservedPath(directory) {
		(&refusal{http.StatusForbidden, CodePathReserved, "路径 " + directory + " 是传输暂存区，不能作为上传目标"}).write(w)
		return
	}
	if !validUploadName(request.Name) {
		(&refusal{http.StatusBadRequest, CodeTransferInvalid, "文件名无效：" + request.Name}).write(w)
		return
	}
	if directory == "" && request.Name == StagingDir {
		(&refusal{http.StatusForbidden, CodePathReserved, "名称 " + StagingDir + " 是传输暂存区，不能作为上传目标"}).write(w)
		return
	}
	if *request.Size < 0 {
		(&refusal{http.StatusBadRequest, CodeTransferInvalid, "上传大小不能为负数"}).write(w)
		return
	}
	address, rejected := opened.address(directory)
	if rejected != nil {
		rejected.write(w)
		return
	}
	info, err := opened.root.Stat(address)
	if err != nil {
		s.log.Warn("stat the upload directory", "volume", namespace+"/"+name, "path", directory, "error", err)
		(&refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内目录失败"}).write(w)
		return
	}
	if !info.IsDir() {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "路径 " + directory + " 是文件，不能作为上传目录"}).write(w)
		return
	}
	target := request.Name
	if directory != "" {
		target = directory + "/" + request.Name
	}
	conflict, rejected := existingTarget(opened, target)
	if rejected != nil {
		rejected.write(w)
		return
	}
	accepted := conflict == nil || request.Overwrite
	if accepted {
		if err := opened.root.Mkdir(StagingDir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			s.log.Warn("create the staging directory", "volume", namespace+"/"+name, "error", err)
			(&refusal{http.StatusInternalServerError, CodeFilesUnreadable, "无法在卷上创建传输暂存区"}).write(w)
			return
		}
	}
	now := s.now().UTC()
	session := &transfer{
		Transfer: Transfer{
			ID:        newTransferID(),
			Direction: "upload",
			Volume:    namespace + "/" + name,
			Path:      directory,
			Name:      request.Name,
			Target:    target,
			State:     TransferStateOpen,
			Size:      *request.Size,
			Overwrite: request.Overwrite,
			Conflict:  conflict,
			CreatedAt: now,
			UpdatedAt: now,
		},
		namespace:  namespace,
		volumeName: name,
	}
	session.staging = StagingDir + "/" + session.ID + ".part"
	if conflict != nil && !request.Overwrite {
		session.State = TransferStateConflict
		session.Reason = "目标位置已存在同名文件 " + target + "：确认覆盖后才能上传"
	}
	s.keepTransfer(session)
	w.Header().Set("Cache-Control", "no-store")
	if session.State == TransferStateOpen && session.Size == 0 {
		file, err := opened.root.OpenFile(session.staging, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			s.log.Warn("stage the empty upload", "volume", namespace+"/"+name, "error", err)
			session.own.Lock()
			session.State = TransferStateFailed
			session.Reason = "无法写入传输暂存区"
			session.UpdatedAt = s.now().UTC()
			answer := session.copyLocked()
			session.own.Unlock()
			writeBody(w, http.StatusInternalServerError, map[string]any{
				"error":    map[string]any{"code": CodeFilesUnreadable, "message": session.Reason},
				"transfer": answer,
			})
			return
		}
		_ = file.Close()
		if rejected := s.finishUpload(opened, session); rejected != nil {
			writeBody(w, rejected.status, map[string]any{
				"error":    map[string]any{"code": rejected.code, "message": rejected.message},
				"transfer": session.snapshot(),
			})
			return
		}
	}
	writeJSON(w, http.StatusCreated, session.snapshot())
}

// handleTransferProgress answers how much of one session has arrived. Received
// is the progress the window shows, and it equals the declared size once the
// session completed (FILE-C-4).
func (s *Service) handleTransferProgress(w http.ResponseWriter, r *http.Request) {
	binding := bindingOf(r.Context())
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	session, rejected := s.lookupTransfer(namespace, name, r.PathValue("transfer"))
	if rejected != nil {
		rejected.write(w)
		return
	}
	if _, rejected := s.readVolume(r.Context(), binding, namespace, name); rejected != nil {
		rejected.write(w)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, session.snapshot())
}

// handleTransferContent appends the request body to one open session. The
// bytes go to the staging file, never to the target name, so a cancelled or
// failed session leaves no partial file there (FILE-C-4).
func (s *Service) handleTransferContent(w http.ResponseWriter, r *http.Request) {
	binding := bindingOf(r.Context())
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	session, rejected := s.lookupTransfer(namespace, name, r.PathValue("transfer"))
	if rejected != nil {
		rejected.write(w)
		return
	}
	opened, rejected := s.openVolumeRoot(r.Context(), binding, namespace, name)
	if rejected != nil {
		rejected.write(w)
		return
	}
	defer opened.Close()
	session.own.Lock()
	switch session.State {
	case TransferStateOpen:
	case TransferStateConflict:
		message := session.Reason
		answer := session.copyLocked()
		session.own.Unlock()
		writeBody(w, http.StatusConflict, map[string]any{
			"error":    map[string]any{"code": CodeUploadConflict, "message": message},
			"transfer": answer,
		})
		return
	default:
		answer := session.copyLocked()
		session.own.Unlock()
		writeBody(w, http.StatusConflict, map[string]any{
			"error":    map[string]any{"code": CodeTransferClosed, "message": "传输已结束，不能再接收内容"},
			"transfer": answer,
		})
		return
	}
	remaining := session.Size - session.Received
	file, err := opened.root.OpenFile(session.staging, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		s.discardStaging(opened, session)
		session.State = TransferStateFailed
		session.Reason = "无法写入传输暂存区"
		session.UpdatedAt = s.now().UTC()
		answer := session.copyLocked()
		session.own.Unlock()
		s.log.Warn("open the staged file", "transfer", session.ID, "error", err)
		writeBody(w, http.StatusInternalServerError, map[string]any{
			"error":    map[string]any{"code": CodeFilesUnreadable, "message": session.Reason},
			"transfer": answer,
		})
		return
	}
	written, copyErr := io.Copy(file, io.LimitReader(r.Body, remaining+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		s.discardStaging(opened, session)
		session.State = TransferStateFailed
		session.Reason = "上传中断：内容没有收全，目标位置未写入"
		session.UpdatedAt = s.now().UTC()
		answer := session.copyLocked()
		session.own.Unlock()
		s.log.Warn("receive the upload", "transfer", session.ID, "error", errors.Join(copyErr, syncErr, closeErr))
		writeBody(w, http.StatusBadRequest, map[string]any{
			"error":    map[string]any{"code": CodeTransferSizeMismatch, "message": session.Reason},
			"transfer": answer,
		})
		return
	}
	if written > remaining {
		s.discardStaging(opened, session)
		session.State = TransferStateFailed
		session.Reason = "上传的数据超过声明的大小 " + strconv.FormatInt(session.Size, 10) + " 字节"
		session.UpdatedAt = s.now().UTC()
		answer := session.copyLocked()
		session.own.Unlock()
		writeBody(w, http.StatusConflict, map[string]any{
			"error":    map[string]any{"code": CodeTransferSizeMismatch, "message": session.Reason},
			"transfer": answer,
		})
		return
	}
	session.Received += written
	session.UpdatedAt = s.now().UTC()
	if session.Received < session.Size {
		answer := session.copyLocked()
		session.own.Unlock()
		writeJSON(w, http.StatusAccepted, answer)
		return
	}
	rejected = s.finishUploadLocked(opened, session)
	answer := session.copyLocked()
	session.own.Unlock()
	if rejected != nil {
		writeBody(w, rejected.status, map[string]any{
			"error":    map[string]any{"code": rejected.code, "message": rejected.message},
			"transfer": answer,
		})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, answer)
}

// handleTransferCancel stops one session and removes its staged bytes. The
// target name is left exactly as it was (FILE-C-4).
func (s *Service) handleTransferCancel(w http.ResponseWriter, r *http.Request) {
	binding := bindingOf(r.Context())
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	session, rejected := s.lookupTransfer(namespace, name, r.PathValue("transfer"))
	if rejected != nil {
		rejected.write(w)
		return
	}
	opened, rejected := s.openVolumeRoot(r.Context(), binding, namespace, name)
	if rejected != nil {
		rejected.write(w)
		return
	}
	defer opened.Close()
	session.own.Lock()
	switch session.State {
	case TransferStateOpen, TransferStateConflict:
		s.discardStaging(opened, session)
		session.State = TransferStateCancelled
		session.UpdatedAt = s.now().UTC()
	case TransferStateCancelled:
		// Stopping twice is the outcome the window asked for.
	default:
		answer := session.copyLocked()
		session.own.Unlock()
		writeBody(w, http.StatusConflict, map[string]any{
			"error":    map[string]any{"code": CodeTransferClosed, "message": "传输已结束，不能取消"},
			"transfer": answer,
		})
		return
	}
	answer := session.copyLocked()
	session.own.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, answer)
}

// handleDownload streams one file of one volume with its size declared, so the
// window can show progress and knows the total (FILE-C-4). A missing or
// unreadable file is refused with its reason instead of an empty stream.
func (s *Service) handleDownload(w http.ResponseWriter, r *http.Request) {
	binding := bindingOf(r.Context())
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	relative, ok := cleanPath(r.URL.Query().Get("path"))
	if !ok {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "路径无效：不能是绝对路径或包含上级目录"}).write(w)
		return
	}
	if relative == "" {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "卷根是目录，不能下载"}).write(w)
		return
	}
	if reservedPath(relative) {
		(&refusal{http.StatusForbidden, CodePathReserved, "路径 " + relative + " 是传输暂存区，不能下载"}).write(w)
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
	info, err := opened.root.Stat(address)
	if err != nil {
		s.log.Warn("stat the download file", "volume", namespace+"/"+name, "path", relative, "error", err)
		(&refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内文件失败"}).write(w)
		return
	}
	if info.IsDir() {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "路径 " + relative + " 是目录，不能下载"}).write(w)
		return
	}
	if !info.Mode().IsRegular() {
		(&refusal{http.StatusUnsupportedMediaType, CodeDownloadUnsupported,
			"路径 " + relative + " 不是普通文件，不能下载"}).write(w)
		return
	}
	file, err := opened.root.Open(address)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			(&refusal{http.StatusForbidden, CodeFileUnreadable, "文件 " + relative + " 不可读：权限不足"}).write(w)
			return
		}
		s.log.Warn("open the download file", "volume", namespace+"/"+name, "path", relative, "error", err)
		(&refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内文件失败"}).write(w)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", downloadType(relative))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(relative)}))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, file); err != nil {
		// The answer already started; the window sees a short read.
		s.log.Warn("stream the download", "volume", namespace+"/"+name, "path", relative, "error", err)
	}
}

// transferRequest is the document that opens one session.
type transferRequest struct {
	// Direction is upload; absent means upload.
	Direction string `json:"direction"`
	// Path is the browsed directory inside the volume.
	Path string `json:"path"`
	// Name is the file name at the target.
	Name string `json:"name"`
	// Size is the declared content size in bytes.
	Size *int64 `json:"size"`
	// Overwrite confirms replacing a same-name file.
	Overwrite bool `json:"overwrite"`
}

// decodeTransferRequest reads the session-opening document, refusing anything
// this surface cannot start a session for.
func decodeTransferRequest(w http.ResponseWriter, r *http.Request) (transferRequest, *refusal) {
	request := transferRequest{Direction: "upload"}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxTransferRequestBytes))
	if err := decoder.Decode(&request); err != nil {
		return transferRequest{}, &refusal{http.StatusBadRequest, CodeTransferInvalid, "请求体不是有效的传输请求"}
	}
	if request.Direction != "upload" {
		return transferRequest{}, &refusal{http.StatusBadRequest, CodeTransferInvalid, "不支持的传输方向：" + request.Direction}
	}
	if request.Size == nil {
		return transferRequest{}, &refusal{http.StatusBadRequest, CodeTransferInvalid, "缺少上传大小"}
	}
	return request, nil
}

// existingTarget describes the entry already at one target path, and refuses a
// target that cannot hold a file at all.
func existingTarget(opened *volumeRoot, target string) (*TransferConflict, *refusal) {
	address, rejected := opened.address(target)
	if rejected != nil {
		if rejected.code == CodePathNotFound {
			return nil, nil
		}
		return nil, rejected
	}
	info, err := opened.root.Stat(address)
	if err != nil {
		return nil, &refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内目标位置失败"}
	}
	if info.IsDir() {
		return nil, &refusal{http.StatusBadRequest, CodePathInvalid, "目标位置已存在同名目录 " + target + "，不能作为上传目标"}
	}
	return &TransferConflict{Size: info.Size(), ModifiedAt: info.ModTime().UTC()}, nil
}

// finishUpload settles one session that already received its whole content.
func (s *Service) finishUpload(opened *volumeRoot, session *transfer) *refusal {
	session.own.Lock()
	defer session.own.Unlock()
	return s.finishUploadLocked(opened, session)
}

// finishUploadLocked settles one session whose lock the caller holds. The
// target is re-checked, so a same-name file another writer created during the
// transfer is never replaced without confirmation (FILE-C-4).
func (s *Service) finishUploadLocked(opened *volumeRoot, session *transfer) *refusal {
	if !session.Overwrite {
		if _, rejected := opened.address(session.Target); rejected == nil {
			s.discardStaging(opened, session)
			session.State = TransferStateConflict
			session.Reason = "目标位置在上传期间出现同名文件 " + session.Target + "：确认覆盖后才能上传"
			session.UpdatedAt = s.now().UTC()
			return &refusal{http.StatusConflict, CodeUploadConflict, session.Reason}
		} else if rejected.code != CodePathNotFound {
			s.discardStaging(opened, session)
			session.State = TransferStateFailed
			session.Reason = rejected.message
			session.UpdatedAt = s.now().UTC()
			return rejected
		}
	}
	if err := opened.root.Rename(session.staging, insidePath(session.Target)); err != nil {
		s.discardStaging(opened, session)
		session.State = TransferStateFailed
		session.Reason = "写入目标位置失败：" + session.Target
		session.UpdatedAt = s.now().UTC()
		s.log.Warn("move the staged file into place", "transfer", session.ID, "error", err)
		return &refusal{http.StatusInternalServerError, CodeFilesUnreadable, session.Reason}
	}
	now := s.now().UTC()
	session.State = TransferStateCompleted
	session.CompletedAt = &now
	session.UpdatedAt = now
	return nil
}

// discardStaging removes one session's staged bytes; the target is untouched.
func (s *Service) discardStaging(opened *volumeRoot, session *transfer) {
	if session.staging == "" {
		return
	}
	if err := opened.root.Remove(session.staging); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("remove the staged file", "transfer", session.ID, "error", err)
	}
}

// lookupTransfer reads the session named in one volume's path. A session of
// another volume is not this volume's session.
func (s *Service) lookupTransfer(namespace, name, id string) (*transfer, *refusal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, found := s.transfers[id]
	if !found || session.namespace != namespace || session.volumeName != name {
		return nil, &refusal{http.StatusNotFound, CodeTransferNotFound, "传输 " + id + " 不存在"}
	}
	return session, nil
}

// keepTransfer records one session and drops the oldest settled one when the
// bound is reached.
func (s *Service) keepTransfer(session *transfer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.transfers) >= MaxTransfers {
		var oldest *transfer
		for _, candidate := range s.transfers {
			switch candidate.state() {
			case TransferStateCompleted, TransferStateCancelled, TransferStateFailed:
				if oldest == nil || candidate.CreatedAt.Before(oldest.CreatedAt) {
					oldest = candidate
				}
			}
		}
		if oldest != nil {
			delete(s.transfers, oldest.ID)
		}
	}
	s.transfers[session.ID] = session
}

// newTransferID draws one session identity.
func newTransferID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buffer)
}

// reservedPath reports whether one relative path addresses the staging area.
func reservedPath(relative string) bool {
	return relative == StagingDir || strings.HasPrefix(relative, StagingDir+"/")
}

// validUploadName reports whether one file name may be written at the target.
func validUploadName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > MaxNameLength {
		return false
	}
	return !strings.ContainsAny(name, `/\`) && !strings.ContainsRune(name, 0)
}

// downloadType names the media type one download is sent as.
func downloadType(relative string) string {
	if named := mime.TypeByExtension(strings.ToLower(path.Ext(relative))); named != "" {
		return named
	}
	return "application/octet-stream"
}
