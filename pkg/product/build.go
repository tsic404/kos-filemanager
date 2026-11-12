package product

import (
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"strings"

	"github.com/tsic404/kos-sdk/packaging"
)

// ui holds the document, the window module and the stylesheet the window runs,
// plus the icon the manifest names. Every one of them is a package UI asset, so
// the digest covers what a window actually loads (KOSD-C-73, SDK-C-4).
//
//go:embed assets
var ui embed.FS

// AssetDir is the directory inside the package that carries UI assets
// (KOSD-C-73, SDK-C-4).
const AssetDir = packaging.AssetPrefix

// Assets returns the files the package carries, each named by its
// package-relative path.
func Assets() ([]packaging.Asset, error) {
	entries, err := fs.ReadDir(ui, "assets")
	if err != nil {
		return nil, fmt.Errorf("read the embedded assets: %w", err)
	}
	assets := make([]packaging.Asset, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := ui.ReadFile(path.Join("assets", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read asset %s: %w", entry.Name(), err)
		}
		assets = append(assets, packaging.Asset{
			Path:    packaging.AssetPrefix + entry.Name(),
			Content: content,
		})
	}
	return assets, nil
}

// Build validates the declaration and packs it with the carried assets. The
// returned bytes are the artifact an install path receives; the digest covers
// the manifest and every asset (SDK-C-4, KOSD-C-17).
func Build(origin string) (packaging.Package, []byte, error) {
	entry, err := Entry(origin)
	if err != nil {
		return packaging.Package{}, nil, err
	}
	assets, err := Assets()
	if err != nil {
		return packaging.Package{}, nil, err
	}
	if err := checkEntryCarried(entry, assets); err != nil {
		return packaging.Package{}, nil, err
	}
	return packaging.Pack(Declaration(entry), assets)
}

// Entry turns the build origin into the entry address the manifest declares.
// The address is the window's own document on an origin of its own: the host
// reads the path as the package asset the frame loads, so it names the carried
// document rather than the origin alone (KOSD-C-73).
func Entry(origin string) (string, error) {
	raw := strings.TrimSpace(origin)
	if raw == "" {
		raw = DefaultEntryOrigin
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("entry origin %q is not an address: %w", origin, err)
	}
	if !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" {
		return "", fmt.Errorf("entry origin %q is not an absolute https origin (KOSD-C-73)", origin)
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("entry origin %q must be an origin only, without credentials or path (KOSD-C-73)", origin)
	}
	return parsed.Scheme + "://" + parsed.Host + "/" + DocumentAsset, nil
}

// Origin returns the origin an entry address belongs to. Every address this
// product declares — the document, its sub-assets and the backend health probe —
// lives on that one origin (KOSD-C-73).
func Origin(entry string) (string, error) {
	parsed, err := url.Parse(entry)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("entry %q is not an absolute https address (KOSD-C-73)", entry)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("entry %q must not carry credentials (KOSD-C-73)", entry)
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host), nil
}

// checkEntryCarried refuses an entry whose path names no asset the package
// carries: the frame would load a document the artifact does not ship.
func checkEntryCarried(entry string, assets []packaging.Asset) error {
	parsed, err := url.Parse(entry)
	if err != nil {
		return fmt.Errorf("entry %q is not an address: %w", entry, err)
	}
	named := strings.TrimPrefix(path.Clean("/"+parsed.Path), "/")
	for _, asset := range assets {
		if asset.Path == named {
			return nil
		}
	}
	return fmt.Errorf("entry %q names %q, which the package does not carry (KOSD-C-73)", entry, named)
}
