package product_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tsic404/kos-sdk/manifest"
	"github.com/tsic404/kos-sdk/packaging"
	"github.com/tsic404/kos-sdk/validate"

	"github.com/tsic404/kos-filemanager/pkg/product"
)

// TestPackageIsVerifiableAndCarriesTheDeclaration checks the artifact an
// install path receives: the SDK verifies it, the digest covers the manifest,
// and the declaration still names every contribution (SDK-C-4, KOSD-C-17).
func TestPackageIsVerifiableAndCarriesTheDeclaration(t *testing.T) {
	pkg, archive, err := product.Build("")
	if err != nil {
		t.Fatalf("build the package: %v", err)
	}
	verified, err := packaging.Verify(archive, pkg.Digest)
	if err != nil {
		t.Fatalf("verify the package: %v", err)
	}
	if verified.Digest != pkg.Digest {
		t.Fatalf("verified digest %s, built %s", verified.Digest, pkg.Digest)
	}
	if !strings.HasPrefix(string(pkg.Digest), "sha256:") {
		t.Fatalf("digest %q is not in sha256:<hex> form", pkg.Digest)
	}

	carried := make(map[string]bool, len(verified.Assets))
	for _, asset := range verified.Assets {
		carried[asset.Path] = true
	}
	if !carried[product.IconAsset] {
		t.Fatalf("package assets = %v, want the declared icon %s", carried, product.IconAsset)
	}
	for _, assetPath := range []string{product.DocumentAsset, product.ModuleAsset, product.StyleAsset} {
		if !carried[assetPath] {
			t.Fatalf("package assets = %v, want the window asset %s", carried, assetPath)
		}
	}

	if verified.Manifest.Identifier != product.Identifier || verified.Manifest.Version != product.Version {
		t.Fatalf("manifest names %s@%s, want %s@%s",
			verified.Manifest.Identifier, verified.Manifest.Version, product.Identifier, product.Version)
	}
	if verified.Manifest.FrameworkVersion != product.FrameworkVersion {
		t.Fatalf("framework version %q, want %q", verified.Manifest.FrameworkVersion, product.FrameworkVersion)
	}
}

// TestAppManifestSatisfiesTheWindowContract checks the fields KOSD-C-73 fixes,
// field by field, against the frozen values the package declares.
func TestAppManifestSatisfiesTheWindowContract(t *testing.T) {
	pkg, _, err := product.Build(product.DefaultEntryOrigin)
	if err != nil {
		t.Fatalf("build the package: %v", err)
	}
	if len(pkg.Manifest.Contributions.Apps) != 1 {
		t.Fatalf("the package declares %d applications, want one (KOSD-C-73)",
			len(pkg.Manifest.Contributions.Apps))
	}
	app := pkg.Manifest.Contributions.Apps[0]
	if app.ID != product.AppID || app.Version != product.Version {
		t.Fatalf("app declares %s@%s, want %s@%s", app.ID, app.Version, product.AppID, product.Version)
	}
	if app.Icon != product.IconAsset {
		t.Fatalf("icon = %q, want the carried asset %q", app.Icon, product.IconAsset)
	}
	if app.Entry != "https://filemanager.kos.local/"+product.DocumentAsset {
		t.Fatalf("entry = %q, want the carried document address", app.Entry)
	}
	if app.Window.Width != product.WindowWidth || app.Window.Height != product.WindowHeight ||
		app.Window.MinWidth != product.WindowMinWidth || app.Window.MinHeight != product.WindowMinHeight ||
		app.Window.Resizable != product.WindowResizable || app.Window.Mode != product.WindowModeSingle ||
		app.Window.DefaultState != product.WindowDefaultState {
		t.Fatalf("window = %+v, want the declared defaults", app.Window)
	}
	if app.Backend == nil {
		t.Fatal("the application declares no backend; the data plane would be unreachable (KOSD-C-73)")
	}
	wantHealth := "https://filemanager.kos.local" + product.HealthPath
	if app.Backend.Type != product.BackendType || app.Backend.Health != wantHealth {
		t.Fatalf("backend = %+v, want type %q and health %q",
			*app.Backend, product.BackendType, wantHealth)
	}
	if len(app.Permissions) == 0 {
		t.Fatal("the application declares no permission; the package would claim no capability (KOSD-C-75)")
	}
	for _, permission := range app.Permissions {
		if !isFixedPermission(permission) {
			t.Fatalf("permission %q is outside the fixed set (KOSD-C-75)", permission)
		}
	}
}

// TestDeclarationNamesThePreferencesAndReadScope checks the contribution classes
// the framework loads from the package: the declared preference domain with its
// three keys, the volume read scope the data plane needs, and the uninstall fate
// of the preference data (KOSD-C-18, KOSD-C-29).
func TestDeclarationNamesThePreferencesAndReadScope(t *testing.T) {
	pkg, _, err := product.Build("")
	if err != nil {
		t.Fatalf("build the package: %v", err)
	}
	entry, err := product.Entry(product.DefaultEntryOrigin)
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	declaration := product.Declaration(entry)
	contributions, _ := declaration["contributions"].(map[string]any)
	preferences, _ := contributions["preferences"].([]any)
	if len(preferences) != 1 {
		t.Fatalf("declared preference schemas = %v, want one", preferences)
	}
	schema, _ := preferences[0].(map[string]any)
	if schema["id"] != product.PreferenceDomain {
		t.Fatalf("preference domain = %v, want %q", schema["id"], product.PreferenceDomain)
	}
	keys, _ := schema["keys"].([]any)
	if len(keys) != len(product.PreferenceKeys) {
		t.Fatalf("declared keys = %v, want %v", keys, product.PreferenceKeys)
	}
	for index, key := range product.PreferenceKeys {
		if keys[index] != key {
			t.Fatalf("declared keys = %v, want %v", keys, product.PreferenceKeys)
		}
	}

	permissions, _ := declaration["permissions"].([]any)
	if len(permissions) != 1 {
		t.Fatalf("declared permissions = %v, want the data plane's read scope", permissions)
	}
	permission, _ := permissions[0].(map[string]any)
	scope, _ := permission["readScope"].(map[string]any)
	groups, _ := scope["groups"].([]any)
	if len(groups) != 1 || groups[0] != product.VolumeAPIGroup {
		t.Fatalf("read scope groups = %v, want %q", groups, product.VolumeAPIGroup)
	}
	kinds, _ := scope["kinds"].([]any)
	if len(kinds) != 1 {
		t.Fatalf("read scope kinds = %v, want the volume class", kinds)
	}
	volume, _ := kinds[0].(map[string]any)
	if volume["group"] != product.VolumeAPIGroup || volume["kind"] != product.VolumeKind {
		t.Fatalf("read scope kind = %v, want %s/%s", volume, product.VolumeAPIGroup, product.VolumeKind)
	}
	if pkg.Manifest.Permissions[0].ReadScope.Groups[0] != product.VolumeAPIGroup ||
		pkg.Manifest.Permissions[0].ReadScope.Kinds[0].Kind != product.VolumeKind {
		t.Fatalf("packaged read scope = %+v, want the volume class", pkg.Manifest.Permissions[0].ReadScope)
	}

	if len(pkg.Manifest.Retention) != 1 {
		t.Fatalf("declared retention = %+v, want the preference data's fate", pkg.Manifest.Retention)
	}
	if entry := pkg.Manifest.Retention[0]; entry.Resource != "preferences" || entry.Keep {
		t.Fatalf("retention = %+v, want the preference data deleted at uninstall", entry)
	}
}

// TestEntryNamesTheCarriedDocument checks the build input and the address it
// produces: an origin-only build input becomes the document the package carries,
// on its own origin (KOSD-C-73).
func TestEntryNamesTheCarriedDocument(t *testing.T) {
	testCases := []struct {
		name   string
		origin string
		want   string
		reject bool
	}{
		{name: "default", origin: "", want: "https://filemanager.kos.local/" + product.DocumentAsset},
		{name: "https origin", origin: "https://files.example", want: "https://files.example/" + product.DocumentAsset},
		{name: "plain http", origin: "http://files.example", reject: true},
		{name: "path", origin: "https://files.example/files", reject: true},
		{name: "credentials", origin: "https://user:secret@files.example", reject: true},
		{name: "query", origin: "https://files.example?x=1", reject: true},
		{name: "not an address", origin: "files.example", reject: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			entry, err := product.Entry(testCase.origin)
			if testCase.reject {
				if err == nil {
					t.Fatalf("entry %q was accepted as %q", testCase.origin, entry)
				}
				return
			}
			if err != nil {
				t.Fatalf("entry %q: %v", testCase.origin, err)
			}
			if entry != testCase.want {
				t.Fatalf("entry %q = %q, want %q", testCase.origin, entry, testCase.want)
			}
		})
	}
}

// TestDeclarationRejectsAnUnknownManifestField pins the SDK's judgement as a
// property of this product too: it may not carry a field the contract does not
// define (SDK-C-3).
func TestDeclarationRejectsAnUnknownManifestField(t *testing.T) {
	entry, err := product.Entry(product.DefaultEntryOrigin)
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	declaration := product.Declaration(entry)
	declaration["hiddenField"] = "not a contract field"
	result := manifest.ValidateExtensionManifest(declaration)
	if result.OK() {
		t.Fatal("a declaration with an undefined field was accepted")
	}
	if !hasIssue(result.Issues, "$.hiddenField", validate.CodeUnknownField) {
		t.Fatalf("issues = %v, want an unknown-field issue at $.hiddenField", result.Issues)
	}
}

// isFixedPermission reports one name of the closed capability set (KOSD-C-75).
func isFixedPermission(name manifest.Permission) bool {
	for _, permission := range manifest.AppPermissions {
		if permission == name {
			return true
		}
	}
	return false
}

// hasIssue reports one issue at a path with a code.
func hasIssue(issues []validate.Issue, path string, code validate.Code) bool {
	for _, issue := range issues {
		if issue.Path == path && issue.Code == code {
			return true
		}
	}
	return false
}

// TestAppJSONCarriesTheBackend keeps the manifest readable as wire by a reader
// that does not import the SDK types.
func TestAppJSONCarriesTheBackend(t *testing.T) {
	entry, err := product.Entry(product.DefaultEntryOrigin)
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	raw, err := json.Marshal(product.App(entry))
	if err != nil {
		t.Fatalf("encode the application manifest: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode the application manifest: %v", err)
	}
	backend, _ := decoded["backend"].(map[string]any)
	wantHealth := "https://filemanager.kos.local" + product.HealthPath
	if backend["type"] != "http" || backend["health"] != wantHealth {
		t.Fatalf("backend = %v, want the declared http health address", backend)
	}
}
