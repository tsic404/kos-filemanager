package dataplane

import (
	"path/filepath"
	"strings"
)

// VolumeRoots maps one volume to the directory this data plane serves for it
// (NG10). The mapping is a deployment fact, not a product one: the service runs
// where the volume is mounted and keeps no index of what is inside.
type VolumeRoots interface {
	// Root returns the directory of one volume; false means the volume is not
	// mounted on this data-plane host.
	Root(namespace, name string) (string, bool)
}

// DirectoryRoots serves one directory per volume below a base directory. Every
// read and preview is confined to that directory (FILE-C-3).
type DirectoryRoots struct {
	// Base is the directory the volume mounts live under.
	Base string
}

// Root returns the volume's directory below Base. A name that could step out of
// the base directory names no volume this data plane serves.
func (d DirectoryRoots) Root(namespace, name string) (string, bool) {
	if d.Base == "" || !isPathSegment(namespace) || !isPathSegment(name) {
		return "", false
	}
	return filepath.Join(d.Base, namespace, name), true
}

// isPathSegment reports one directory component a volume may map to.
func isPathSegment(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	return !strings.ContainsAny(value, `/\`)
}
