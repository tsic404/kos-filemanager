package dataplane

import (
	"context"
	"encoding/base64"
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
	"unicode/utf8"
)

// textTypes are the extensions this service renders as text, with the media
// type each one is shown as. The table is fixed here rather than read from the
// host, so the same file is previewed the same way on every deployment.
var textTypes = map[string]string{
	".txt":      "text/plain; charset=utf-8",
	".log":      "text/plain; charset=utf-8",
	".md":       "text/markdown; charset=utf-8",
	".markdown": "text/markdown; charset=utf-8",
	".csv":      "text/csv; charset=utf-8",
	".tsv":      "text/tab-separated-values; charset=utf-8",
	".json":     "application/json; charset=utf-8",
	".yaml":     "text/yaml; charset=utf-8",
	".yml":      "text/yaml; charset=utf-8",
	".toml":     "text/plain; charset=utf-8",
	".ini":      "text/plain; charset=utf-8",
	".conf":     "text/plain; charset=utf-8",
	".cfg":      "text/plain; charset=utf-8",
	".env":      "text/plain; charset=utf-8",
	".xml":      "text/xml; charset=utf-8",
	".html":     "text/html; charset=utf-8",
	".htm":      "text/html; charset=utf-8",
	".css":      "text/css; charset=utf-8",
	".js":       "text/javascript; charset=utf-8",
	".ts":       "text/plain; charset=utf-8",
	".go":       "text/plain; charset=utf-8",
	".py":       "text/plain; charset=utf-8",
	".sh":       "text/plain; charset=utf-8",
	".sql":      "text/plain; charset=utf-8",
	".svg":      "image/svg+xml; charset=utf-8",
}

// imageTypes are the extensions this service renders as an image.
var imageTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".avif": "image/avif",
}

// Refusals of the file surface (FILE-C-3). They name this service's own
// boundary: the volume root it serves, the type it can render, and the path it
// was asked for.
const (
	// CodePathInvalid refuses a path this service will not address, such as an
	// absolute path or one with a parent step.
	CodePathInvalid = "path_invalid"
	// CodePathOutside refuses a path that leaves the volume root, including
	// through a symbolic link a co-writer of the volume planted (FILE-C-3).
	CodePathOutside = "path_outside_volume"
	// CodePathNotFound refuses a path the volume does not hold.
	CodePathNotFound = "path_not_found"
	// CodeFilesUnavailable refuses a read when this host has no directory for
	// the volume, instead of answering an empty directory.
	CodeFilesUnavailable = "files_unavailable"
	// CodeFilesUnreadable refuses a read the filesystem failed.
	CodeFilesUnreadable = "files_unreadable"
	// CodePreviewUnsupported refuses a preview of a type this service cannot
	// render, with the reason instead of a blank page or a download.
	CodePreviewUnsupported = "preview_unsupported"
)

// Entry kinds a listing reports. A symbolic link keeps its own kind: the
// listing names what the directory holds, and the link is not followed until a
// request addresses its path.
const (
	// KindFile is a regular file.
	KindFile = "file"
	// KindDirectory is a directory.
	KindDirectory = "directory"
	// KindSymlink is a symbolic link.
	KindSymlink = "symlink"
	// KindOther is any other entry kind, such as a device or a socket.
	KindOther = "other"
)

// Bounds on one answer. A listing is cut to MaxDirectoryEntries and marked; a
// text preview is cut to MaxPreviewTextBytes and marked; an image past
// MaxPreviewImageBytes is refused with its reason. The values are this
// service's own limits, not contract numbers.
const (
	// MaxDirectoryEntries bounds one directory listing.
	MaxDirectoryEntries = 500
	// MaxPreviewTextBytes bounds the text one preview carries.
	MaxPreviewTextBytes = 256 << 10
	// MaxPreviewImageBytes bounds the bytes one image preview carries.
	MaxPreviewImageBytes = 8 << 20
)

// Preview kinds: how the window renders the content it received.
const (
	// PreviewText carries decoded text in Content.
	PreviewText = "text"
	// PreviewImage carries a data URL in Content.
	PreviewImage = "image"
)

// Entry is one file or directory below a volume path. The four fields are the
// ones the window shows (FILE-C-3).
type Entry struct {
	// Name is the entry's base name.
	Name string `json:"name"`
	// Kind is file, directory, symlink or other.
	Kind string `json:"kind"`
	// Size is the file size in bytes; zero for a directory.
	Size int64 `json:"size"`
	// ModifiedAt is the entry's modification time.
	ModifiedAt time.Time `json:"modifiedAt"`
}

// Listing is one directory read of one volume. It is read from the volume on
// every request, so another window's change is visible on the next browse
// (FILE-C-3); Total and Truncated say what the bound left out.
type Listing struct {
	// Volume is the namespace/name reference of the volume.
	Volume string `json:"volume"`
	// Path is the directory path inside the volume.
	Path string `json:"path"`
	// ObservedAt is when this service read the directory (KOSD-C-13).
	ObservedAt time.Time `json:"observedAt"`
	// Limit is the bound this listing was cut to.
	Limit int `json:"limit"`
	// Total is how many entries the directory holds.
	Total int `json:"total"`
	// Truncated marks a listing cut to Limit.
	Truncated bool `json:"truncated"`
	// Entries are the directory's contents in name order.
	Entries []Entry `json:"entries"`
}

// Preview is one read-only view of one file (FILE-C-3). Content is the text
// itself for PreviewText and a data URL for PreviewImage.
type Preview struct {
	// Volume is the namespace/name reference of the volume.
	Volume string `json:"volume"`
	// Path is the file path inside the volume.
	Path string `json:"path"`
	// Kind is text or image.
	Kind string `json:"kind"`
	// ContentType is the media type the content is rendered as.
	ContentType string `json:"contentType"`
	// Size is the file size in bytes.
	Size int64 `json:"size"`
	// ModifiedAt is the file's modification time.
	ModifiedAt time.Time `json:"modifiedAt"`
	// ObservedAt is when this service read the file (KOSD-C-13).
	ObservedAt time.Time `json:"observedAt"`
	// Content is the text or the image data URL.
	Content string `json:"content"`
	// Truncated marks text cut to MaxPreviewTextBytes.
	Truncated bool `json:"truncated"`
}

// refusal is one answer this service gives instead of a read.
type refusal struct {
	// status is the HTTP status.
	status int
	// code is the refusal code the window reads.
	code string
	// message is the readable reason it shows.
	message string
}

// write answers one refusal in the envelope the console renders.
func (r *refusal) write(w http.ResponseWriter) {
	writeFailure(w, r.status, r.code, r.message)
}

// volumeRoot is one opened volume: the directory handle every path operation
// goes through, and its resolved directory for the containment check.
type volumeRoot struct {
	root     *os.Root
	resolved string
}

// Close releases the volume's directory handle.
func (v *volumeRoot) Close() error { return v.root.Close() }

// readVolume authorizes one volume read: the volume exists, the token's account
// owns it, and its projection allows a browse (FILE-C-1, FILE-C-2).
func (s *Service) readVolume(ctx context.Context, binding Binding, namespace, name string) (ListedVolume, *refusal) {
	observed, found, err := s.volumes.Get(ctx, namespace, name)
	if err != nil {
		s.log.Warn("read the volume", "volume", namespace+"/"+name, "error", err)
		return ListedVolume{}, &refusal{http.StatusServiceUnavailable, CodeVolumesUnreadable, "读取卷失败，请稍后重试"}
	}
	if !found {
		return ListedVolume{}, &refusal{http.StatusNotFound, CodeVolumeNotFound, "卷 " + namespace + "/" + name + " 不存在"}
	}
	listed, authorized := s.describe(observed, binding.Account, s.now())
	if !authorized {
		return ListedVolume{}, &refusal{http.StatusForbidden, CodeVolumeUnauthorized,
			"卷 " + namespace + "/" + name + " 不在本账号的授权范围"}
	}
	return listed, nil
}

// openVolumeRoot authorizes one volume read and opens the directory this host
// serves for it. A volume without a directory here is refused with its reason
// rather than answered as an empty directory.
func (s *Service) openVolumeRoot(ctx context.Context, binding Binding, namespace, name string) (*volumeRoot, *refusal) {
	listed, rejected := s.readVolume(ctx, binding, namespace, name)
	if rejected != nil {
		return nil, rejected
	}
	if !listed.CanBrowse {
		return nil, &refusal{http.StatusConflict, CodeVolumeUnavailable, listed.Reason}
	}
	if s.roots == nil {
		return nil, &refusal{http.StatusServiceUnavailable, CodeFilesUnavailable,
			"卷 " + namespace + "/" + name + " 的数据面目录未在本服务挂载"}
	}
	directory, mounted := s.roots.Root(namespace, name)
	if !mounted {
		return nil, &refusal{http.StatusServiceUnavailable, CodeFilesUnavailable,
			"卷 " + namespace + "/" + name + " 的数据面目录未在本服务挂载"}
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		s.log.Warn("open the volume directory", "volume", namespace+"/"+name, "error", err)
		return nil, &refusal{http.StatusServiceUnavailable, CodeFilesUnavailable,
			"卷 " + namespace + "/" + name + " 的数据面目录不可用"}
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		_ = root.Close()
		s.log.Warn("resolve the volume directory", "volume", namespace+"/"+name, "error", err)
		return nil, &refusal{http.StatusServiceUnavailable, CodeFilesUnavailable,
			"卷 " + namespace + "/" + name + " 的数据面目录不可用"}
	}
	return &volumeRoot{root: root, resolved: resolved}, nil
}

// address resolves one clean relative path inside the volume. A path the
// directory does not hold is refused as missing; a path whose target lies
// outside the volume, including through a symbolic link, is refused as outside
// and its content is never read (FILE-C-3).
func (v *volumeRoot) address(relative string) (string, *refusal) {
	target, err := filepath.EvalSymlinks(filepath.Join(v.resolved, relative))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", &refusal{http.StatusNotFound, CodePathNotFound, "路径 " + relative + " 不存在"}
		}
		return "", &refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内文件失败"}
	}
	if !withinRoot(v.resolved, target) {
		return "", &refusal{http.StatusForbidden, CodePathOutside,
			"路径 " + relative + " 越出卷根：指向卷外的符号链接不被跟随"}
	}
	return insidePath(relative), nil
}

// handleListing reads one directory of one volume (FILE-C-3). The directory is
// read on every request, so a deletion, a rename or another window's change is
// visible on the next browse.
func (s *Service) handleListing(w http.ResponseWriter, r *http.Request) {
	binding := bindingOf(r.Context())
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	relative, ok := cleanPath(r.URL.Query().Get("path"))
	if !ok {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "路径无效：不能是绝对路径或包含上级目录"}).write(w)
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
		(&refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内目录失败"}).write(w)
		s.log.Warn("stat the directory", "volume", namespace+"/"+name, "path", relative, "error", err)
		return
	}
	if !info.IsDir() {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "路径 " + relative + " 是文件，不能作为目录浏览"}).write(w)
		return
	}
	directory, err := opened.root.Open(address)
	if err != nil {
		(&refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内目录失败"}).write(w)
		s.log.Warn("open the directory", "volume", namespace+"/"+name, "path", relative, "error", err)
		return
	}
	defer directory.Close()
	read, err := directory.ReadDir(-1)
	if err != nil {
		(&refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内目录失败"}).write(w)
		s.log.Warn("read the directory", "volume", namespace+"/"+name, "path", relative, "error", err)
		return
	}
	entries := make([]Entry, 0, len(read))
	for _, candidate := range read {
		info, err := candidate.Info()
		if err != nil {
			// An entry that disappeared between the read and the stat is not
			// part of this listing.
			continue
		}
		entries = append(entries, Entry{
			Name:       candidate.Name(),
			Kind:       entryKind(info),
			Size:       entrySize(info),
			ModifiedAt: info.ModTime().UTC(),
		})
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name < entries[right].Name })
	listing := Listing{
		Volume:     namespace + "/" + name,
		Path:       relative,
		ObservedAt: s.now().UTC(),
		Limit:      MaxDirectoryEntries,
		Total:      len(entries),
		Entries:    entries,
	}
	if len(entries) > MaxDirectoryEntries {
		listing.Entries = entries[:MaxDirectoryEntries]
		listing.Truncated = true
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, listing)
}

// handlePreview reads one file without changing it: the file is opened read
// only, its content and modification time are left as they were, and a type
// this service cannot render is refused with its reason (FILE-C-3).
func (s *Service) handlePreview(w http.ResponseWriter, r *http.Request) {
	binding := bindingOf(r.Context())
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	relative, ok := cleanPath(r.URL.Query().Get("path"))
	if !ok {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "路径无效：不能是绝对路径或包含上级目录"}).write(w)
		return
	}
	if relative == "" {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "卷根是目录，不能预览"}).write(w)
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
		(&refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内文件失败"}).write(w)
		s.log.Warn("stat the file", "volume", namespace+"/"+name, "path", relative, "error", err)
		return
	}
	if info.IsDir() {
		(&refusal{http.StatusBadRequest, CodePathInvalid, "路径 " + relative + " 是目录，不能预览"}).write(w)
		return
	}
	if !info.Mode().IsRegular() {
		(&refusal{http.StatusUnsupportedMediaType, CodePreviewUnsupported,
			"路径 " + relative + " 不是普通文件，不能预览"}).write(w)
		return
	}
	file, err := opened.root.Open(address)
	if err != nil {
		(&refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内文件失败"}).write(w)
		s.log.Warn("open the file", "volume", namespace+"/"+name, "path", relative, "error", err)
		return
	}
	defer file.Close()
	kind, contentType, ok := previewType(relative, file)
	if !ok {
		(&refusal{http.StatusUnsupportedMediaType, CodePreviewUnsupported,
			"不支持预览 " + path.Base(relative) + "（" + describeType(relative, file) + "）：仅支持文本与图片"}).write(w)
		return
	}
	preview := Preview{
		Volume:      namespace + "/" + name,
		Path:        relative,
		Kind:        kind,
		ContentType: contentType,
		Size:        info.Size(),
		ModifiedAt:  info.ModTime().UTC(),
		ObservedAt:  s.now().UTC(),
	}
	if kind == PreviewImage {
		content, err := io.ReadAll(io.LimitReader(file, MaxPreviewImageBytes+1))
		if err != nil {
			(&refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内文件失败"}).write(w)
			s.log.Warn("read the image", "volume", namespace+"/"+name, "path", relative, "error", err)
			return
		}
		if len(content) > MaxPreviewImageBytes {
			(&refusal{http.StatusUnsupportedMediaType, CodePreviewUnsupported,
				"图片 " + path.Base(relative) + " 超过 " + describeBytes(MaxPreviewImageBytes) + "，不支持预览"}).write(w)
			return
		}
		preview.Content = "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(content)
	} else {
		content, err := io.ReadAll(io.LimitReader(file, MaxPreviewTextBytes+1))
		if err != nil {
			(&refusal{http.StatusInternalServerError, CodeFilesUnreadable, "读取卷内文件失败"}).write(w)
			s.log.Warn("read the text", "volume", namespace+"/"+name, "path", relative, "error", err)
			return
		}
		if len(content) > MaxPreviewTextBytes {
			content = content[:MaxPreviewTextBytes]
			preview.Truncated = true
		}
		preview.Content = strings.ToValidUTF8(string(content), string(utf8.RuneError))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, preview)
}

// entryKind names what one directory entry is.
func entryKind(info fs.FileInfo) string {
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return KindSymlink
	case info.IsDir():
		return KindDirectory
	case info.Mode().IsRegular():
		return KindFile
	default:
		return KindOther
	}
}

// entrySize reports the bytes one entry occupies; a directory reports none.
func entrySize(info fs.FileInfo) int64 {
	if info.IsDir() {
		return 0
	}
	return info.Size()
}

// cleanPath turns a request path into a clean relative path. An absolute path
// or a parent step is refused instead of normalised: the name has to stay
// relative before the volume root is asked for it.
func cleanPath(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if strings.HasPrefix(raw, "/") || strings.ContainsRune(raw, 0) {
		return "", false
	}
	segments := make([]string, 0, 3)
	for _, segment := range strings.Split(raw, "/") {
		switch segment {
		case "", ".":
			continue
		case "..":
			return "", false
		default:
			segments = append(segments, segment)
		}
	}
	return strings.Join(segments, "/"), true
}

// withinRoot reports whether one resolved path is the volume root or lies below
// it. Both sides are symlink-resolved absolute paths.
func withinRoot(root, target string) bool {
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(os.PathSeparator))
}

// insidePath turns a clean relative path into the name the volume root accepts:
// root methods address the root directory as ".".
func insidePath(relative string) string {
	if relative == "" {
		return "."
	}
	return relative
}

// previewType decides how one file is rendered, and reports false for a type
// this service does not preview. The extension decides first; a file without a
// known extension is sniffed from its first bytes, and the file is left at its
// beginning for the read that follows.
func previewType(relative string, file *os.File) (string, string, bool) {
	extension := strings.ToLower(path.Ext(relative))
	if contentType, known := textTypes[extension]; known {
		return PreviewText, contentType, true
	}
	if contentType, known := imageTypes[extension]; known {
		return PreviewImage, contentType, true
	}
	head := make([]byte, 512)
	read, err := file.Read(head)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", "", false
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", "", false
	}
	sniffed := http.DetectContentType(head[:read])
	switch {
	case strings.HasPrefix(sniffed, "image/"):
		return PreviewImage, sniffed, true
	case strings.HasPrefix(sniffed, "text/"), strings.HasSuffix(sniffed, "+xml"), sniffed == "application/json":
		return PreviewText, sniffed, true
	default:
		return "", "", false
	}
}

// describeType names the type a refusal is about.
func describeType(relative string, file *os.File) string {
	extension := strings.ToLower(path.Ext(relative))
	if extension != "" {
		return extension
	}
	head := make([]byte, 512)
	read, err := file.Read(head)
	if err != nil && !errors.Is(err, io.EOF) {
		return "未知类型"
	}
	return http.DetectContentType(head[:read])
}

// describeBytes renders a bound the refusal names.
func describeBytes(bound int) string { return strconv.Itoa(bound) + " 字节" }
