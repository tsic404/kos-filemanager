package kosdtc155

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
	"github.com/tsic404/kos-dashboard/pkg/bff"

	"github.com/tsic404/kos-filemanager/pkg/product"
	"github.com/tsic404/kos-filemanager/tests/acceptance/fixtures"
)

// assetOrigin is the origin the package's window is served from. The entry the
// manifest declares lives on it, and the asset addresses the console signs are
// addressed to its host (KOSD-C-73).
const assetOrigin = "https://filemanager.kos.local"

// consoleOrigin is the console the asset service allows to frame a window and
// the origin the signed address is issued for (KOSD-F-18).
const consoleOrigin = "https://console.example.test"

// sealKey is the short-lived credential key the console signs addresses with and
// the asset service verifies them against.
var sealKey = []byte("0123456789abcdef0123456789abcdef")

// TestTC155SignedEntryServesThePackagedAssets checks the window the install
// yields: the entry document and the stylesheet, module and icon the package
// carries are served by the dashboard's own asset service under the versioned
// address, the signed document leaves the same-digest grant its relative
// references load with, and a refused address obtains no content
// (KOSD-C-73, KOSD-C-84, KOSD-AC-72).
func TestTC155SignedEntryServesThePackagedAssets(t *testing.T) {
	pkg, _ := fixtures.ProductArtifact(t)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	// Unpack the artifact the way a deployment publishes it: one directory per
	// extension, application, version and digest, carrying the package's assets
	// under their package-relative paths (apphost.DirAssets, KOSD-C-73).
	root := t.TempDir()
	published := filepath.Join(root, product.Identifier, product.AppID, product.Version, pkg.Digest.String())
	contents := make(map[string][]byte, len(pkg.Assets))
	for _, asset := range pkg.Assets {
		target := filepath.Join(published, filepath.FromSlash(asset.Path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("create the asset directory: %v", err)
		}
		if err := os.WriteFile(target, asset.Content, 0o644); err != nil {
			t.Fatalf("publish asset %s: %v", asset.Path, err)
		}
		contents[asset.Path] = asset.Content
	}

	front, err := bff.New(bff.Config{
		Upstream:      &url.URL{Scheme: "https", Host: "control.example.test"},
		SealKey:       sealKey,
		AssetFiles:    &apphost.DirAssets{Root: root},
		ConsoleOrigin: consoleOrigin,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("build the console front: %v", err)
	}
	handler := front.Handler()

	// The address the console hands the frame names the extension, application,
	// version, digest and the package asset the frame loads (KOSD-C-73).
	address, err := apphost.SignedURL(sealKey, assetOrigin, apphost.Asset{
		Extension: product.Identifier,
		AppID:     product.AppID,
		Version:   product.Version,
		Digest:    pkg.Digest.String(),
		Path:      product.DocumentAsset,
	}, now.Add(apphost.DefaultAssetTTL))
	if err != nil {
		t.Fatalf("sign the entry address: %v", err)
	}
	wantPrefix := assetOrigin + "/app-assets/" + product.Identifier + "/" + product.AppID + "/" +
		product.Version + "/" + pkg.Digest.String() + "/" + product.DocumentAsset + "?"
	if !strings.HasPrefix(address, wantPrefix) {
		t.Fatalf("entry address = %q, want the versioned address %q", address, wantPrefix)
	}

	// The document is served from the package, and its answer leaves the grant
	// the document's relative references ride on (KOSD-C-84).
	entry := serveAsset(t, handler, address, "")
	if entry.Code != http.StatusOK {
		t.Fatalf("entry document = %d %q, want it served", entry.Code, entry.Body.String())
	}
	if got := entry.Body.Bytes(); string(got) != string(contents[product.DocumentAsset]) {
		t.Fatalf("entry document = %q, want the packaged document", got)
	}
	grant := assetGrant(t, entry)
	if grant.Path != apphost.AssetGrantPath || grant.Domain != "" || !grant.HttpOnly || grant.MaxAge <= 0 {
		t.Fatalf("grant = %+v, want an HttpOnly, host-only credential scoped to the asset routes", grant)
	}

	// The stylesheet, module and icon the document references relatively are
	// each served under the same digest: this is the window actually loading,
	// not a fixture asserting its own bytes (KOSD-C-84).
	for _, asset := range []struct {
		path        string
		contentType string
	}{
		{product.StyleAsset, "text/css; charset=utf-8"},
		{product.ModuleAsset, "text/javascript; charset=utf-8"},
		{product.IconAsset, "image/svg+xml"},
	} {
		served := serveAsset(t, handler, subresourceAddress(t, address, asset.path), grant.Value)
		if served.Code != http.StatusOK {
			t.Fatalf("%s = %d %q, want the same-digest subresource served", asset.path, served.Code, served.Body.String())
		}
		if string(served.Body.Bytes()) != string(contents[asset.path]) {
			t.Fatalf("%s = %q, want the packaged asset", asset.path, served.Body.Bytes())
		}
		if contentType := served.Header().Get("Content-Type"); contentType != asset.contentType {
			t.Fatalf("%s Content-Type = %q, want %q", asset.path, contentType, asset.contentType)
		}
	}

	// A relative reference that arrives without the grant, and one that presents
	// the document's grant for another digest, obtain no content (KOSD-C-84).
	if refused := serveAsset(t, handler, subresourceAddress(t, address, product.StyleAsset), ""); refused.Code != http.StatusForbidden {
		t.Fatalf("ungranted stylesheet = %d %q, want the address refused", refused.Code, refused.Body.String())
	}
	otherDigest := strings.Replace(address, pkg.Digest.String(), "sha256:"+strings.Repeat("0", 64), 1)
	if refused := serveAsset(t, handler, subresourceAddress(t, otherDigest, product.StyleAsset), grant.Value); refused.Code != http.StatusForbidden {
		t.Fatalf("stylesheet under another digest = %d %q, want the address refused", refused.Code, refused.Body.String())
	}

	// The window's external shape is unchanged: the document references its three
	// sub-assets relatively, on the origin the manifest's entry names.
	document := string(contents[product.DocumentAsset])
	for _, reference := range []string{`href="app.css"`, `src="app.js"`, `href="icon.svg"`} {
		if !strings.Contains(document, reference) {
			t.Fatalf("document lacks the relative reference %s", reference)
		}
	}
	if strings.Contains(document, assetOrigin) {
		t.Fatalf("document = %q, want no absolute origin in its references", document)
	}
}

// serveAsset sends one asset request through the console front's own handler,
// addressed to the application origin, carrying grant when it is not empty.
func serveAsset(t *testing.T, handler http.Handler, address, grant string) *httptest.ResponseRecorder {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse the asset address %q: %v", address, err)
	}
	request := httptest.NewRequest(http.MethodGet, parsed.Path+"?"+parsed.RawQuery, nil)
	request.Host = parsed.Host
	if grant != "" {
		request.AddCookie(&http.Cookie{Name: apphost.AssetGrantCookie, Value: grant})
	}
	served := httptest.NewRecorder()
	handler.ServeHTTP(served, request)
	return served
}

// subresourceAddress rewrites one signed address to a sibling the way a browser
// resolves a relative reference: same directory, digest and version, no query.
func subresourceAddress(t *testing.T, address, assetPath string) string {
	t.Helper()
	parsed, err := url.Parse(address)
	if err != nil {
		t.Fatalf("parse the asset address %q: %v", address, err)
	}
	parsed.RawQuery = ""
	prefix := strings.TrimSuffix(parsed.Path, "/"+product.DocumentAsset)
	parsed.Path = prefix + "/" + assetPath
	return parsed.String()
}

// assetGrant reads the credential a served document left for its relative
// references.
func assetGrant(t *testing.T, served *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, cookie := range served.Result().Cookies() {
		if cookie.Name == apphost.AssetGrantCookie {
			return cookie
		}
	}
	t.Fatalf("served document set no %s cookie, want the same-digest grant", apphost.AssetGrantCookie)
	return nil
}
