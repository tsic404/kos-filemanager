// Package product is the kos-filemanager first-party package: its identity, the
// window it opens as, the preferences it declares and the data-plane backend it
// announces (KOSD-C-17, KOSD-C-18, KOSD-C-73).
//
// The package carries no product-specific install path: it is packed by the SDK
// like any third-party extension, and the framework loads its declaration.
package product

import "github.com/tsic404/kos-sdk/manifest"

// Identity and version of the package (KOSD-C-17). The application identity is
// unique among installed extensions (KOSD-C-73); the preference domain is the
// extension identity, so both surfaces name the same product (DASH-C-27).
const (
	// Identifier is the extension identity across versions.
	Identifier = "kos-filemanager"
	// Version is the package version this declaration belongs to.
	Version = "1.0.0"
	// FrameworkVersion is the framework API range the declaration targets.
	FrameworkVersion = ">=1.0 <2.0"
	// AppID is the window application identity inside this extension.
	AppID = "kos-filemanager"
	// AppName is the label the launcher and dock show.
	AppName = "文件管理"
	// PreferenceDomain is the extension domain the preferences are stored
	// under (DASH-C-27).
	PreferenceDomain = Identifier
)

// Window defaults (KOSD-C-73). The file browser opens as one window per
// account, large enough for a directory list and its sidebar.
const (
	WindowWidth        = 1200
	WindowHeight       = 800
	WindowMinWidth     = 720
	WindowMinHeight    = 480
	WindowResizable    = true
	WindowModeSingle   = "single"
	WindowDefaultState = "normal"
)

// Backend declaration (KOSD-C-73): the data plane is this product's own
// service, reached on the application origin. The type names the protocol
// family; health is the absolute address the console probes, so the window host
// does not have to guess the origin's scheme.
const (
	BackendType = "http"
	HealthPath  = "/healthz"
	HealthCode  = 200
)

// The kernel object class the data plane reads (KOSD-C-19, FILE-C-1). The
// declaration names them in its read scope; the data plane itself reads volumes
// through the control plane and never writes them.
const (
	// VolumeAPIGroup is the API group the volume class belongs to.
	VolumeAPIGroup = "kos.tsic.top"
	// VolumeKind is the object class carrying volume ownership and status.
	VolumeKind = "Volume"
)

// Data-plane permission names (KOSD-C-75). Copy and paste of file entries are
// the capabilities the window uses; notifications and external links are not.
var Permissions = []manifest.Permission{
	manifest.PermissionClipboardRead,
	manifest.PermissionClipboardWrite,
}

// Preference keys in the product's domain (DASH-C-27, FILE-C-1). The first is
// display state: a reference to a volume the account did not own never widens
// the data plane's authorization and never enters the window's volume list.
const (
	// PreferenceExposedVolumes names the volumes the account pinned in the
	// settings surface.
	PreferenceExposedVolumes = "exposedVolumes"
	// PreferenceView is the browser view the window opens with.
	PreferenceView = "view"
	// PreferenceInitialPath is the path the window opens at.
	PreferenceInitialPath = "initialPath"
)

// PreferenceKeys lists the declared keys in the order the declaration names
// them.
var PreferenceKeys = []string{
	PreferenceExposedVolumes,
	PreferenceView,
	PreferenceInitialPath,
}

// Views a window may open with (DASH-C-27). An unknown stored value falls back
// to the list view instead of failing the window.
const (
	ViewList    = "list"
	ViewGrid    = "grid"
	DefaultView = ViewList
)

// DefaultEntryOrigin is the origin the release builds against when no origin is
// given. The origin is a build input, not a runtime setting: the manifest's entry
// is the document on this origin, and the digest covers it (KOSD-C-73).
const DefaultEntryOrigin = "https://filemanager.kos.local"

// UI assets the package carries, named by their package-relative paths. The
// manifest's entry names the document, its icon names the icon, and the document
// loads the module and stylesheet relatively; Build refuses an entry naming
// no carried asset (KOSD-C-73, SDK-C-4).
const (
	// IconAsset is the icon the manifest names.
	IconAsset = "assets/icon.svg"
	// DocumentAsset is the window's entry document.
	DocumentAsset = "assets/index.html"
	// ModuleAsset is the window module the document loads.
	ModuleAsset = "assets/app.js"
	// StyleAsset is the stylesheet the document loads.
	StyleAsset = "assets/app.css"
)
