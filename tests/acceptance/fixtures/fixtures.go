// Package fixtures boots the facts the acceptance cases share: an API server
// with the KOS object model, the control plane and account surface in front of
// it, this product's contributions loaded through the framework's install pass,
// accounts, volumes, the window host that mints application tokens, and the
// product's data plane behind an HTTP server.
package fixtures

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
	"github.com/tsic404/kos-sdk/packaging"
	"github.com/tsic404/kosd/api/v1beta1"
	"github.com/tsic404/kosd/pkg/access"
	"github.com/tsic404/kosd/pkg/apps"
	"github.com/tsic404/kosd/pkg/controlplane"
	"github.com/tsic404/kosd/pkg/extension"
	"github.com/tsic404/kosd/pkg/identity"
	"github.com/tsic404/kosd/pkg/write"
	"github.com/tsic404/kosd/tests/acceptance/harness"

	"github.com/tsic404/kos-filemanager/pkg/dataplane"
	"github.com/tsic404/kos-filemanager/pkg/product"
)

// The accounts the cases sign in as. The global Owner transfers ownership;
// the two members own the files the data plane serves.
const (
	OwnerName     = "owner"
	OwnerPassword = "owner-password"
	MemberOne     = "alice"
	MemberOnePass = "alice-password"
	MemberTwo     = "bob"
	MemberTwoPass = "bob-password"
)

// ThirdPartyComponent is a second package the install cases load alongside this
// product, so a product's withdrawal can be observed against a peer that must
// keep serving (KOSD-C-17).
const ThirdPartyComponent = "third-party-reporting"

// Case is one booted product: the cluster, the control plane, this product's
// loaded contributions and the data plane that serves them.
type Case struct {
	// Cluster is the API server the objects live on.
	Cluster *harness.Cluster
	// Accounts is the account surface in front of the control plane.
	Accounts *harness.Identity
	// Registry is the contribution projection every surface reads.
	Registry *harness.RegistrySource
	// OwnerToken is the global Owner's control-plane session.
	OwnerToken string
	// Artifacts holds the packages the cases install.
	Artifacts extension.MemoryArtifacts
	// Installation is the install pass that loaded this product.
	Installation *extension.Installation
	// Files is the base directory the data plane serves volume directories
	// from; cases write the volume's files below it (FILE-C-3).
	Files string
	// Now is the clock the control plane and the data plane share.
	Now func() time.Time

	host     *apphost.Host
	registry *apphost.Registry
	server   *httptest.Server
}

// Window is one open application window instance and the token it received.
type Window struct {
	// ID is the window instance identity.
	ID string
	// Token is the application token the handshake delivered.
	Token string
}

// Start boots the shared facts and loads this product's contributions.
func Start(t *testing.T) *Case {
	t.Helper()
	cluster := harness.Start(t)
	registry := &harness.RegistrySource{}
	accounts := harness.OpenIdentity(t, nil, func(config *identity.Config) {
		config.Preferences = registry
	})
	server, err := controlplane.New(controlplane.Config{
		Dynamic:    cluster.Dynamic,
		Extensions: registry,
		Authorizer: identity.Authorizer{Policy: &access.Policy{Objects: controlplane.NewClusterSource(cluster.Dynamic)}},
		Auditor:    identity.AuditTrail{Store: accounts.Store},
		Log:        slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatalf("build the control plane: %v", err)
	}
	t.Cleanup(server.Close)
	// The application projection is the control plane's own route; the window
	// host and the desktop read the installed applications from it (KOSD-C-18).
	mux := http.NewServeMux()
	mux.Handle("/api/v1/apps", apps.NewAPI(apps.New(registry)).Handler())
	mux.Handle("/", server.Handler())
	accounts.Serve(t, mux)

	oneTime := accounts.BootstrapOwner(t, OwnerName)
	token, _ := accounts.SignIn(t, OwnerName, oneTime)
	accounts.ChangePassword(t, token, oneTime, OwnerPassword)
	owner, _ := accounts.SignIn(t, OwnerName, OwnerPassword)

	artifacts := extension.MemoryArtifacts{}
	pkg, archive := ProductArtifact(t)
	installed := InstallArtifact(t, cluster, artifacts, product.Identifier, pkg.Digest.String(), archive, accounts.Service)
	registry.Publish(installed.Registry())

	now := accounts.Clock.Now
	host := apphost.New(apphost.Config{Now: now})
	applications := LoadRegistry(t, installed)
	files := t.TempDir()
	service, err := dataplane.New(dataplane.Config{
		Tokens:      dataplane.AppHost{Host: host},
		Volumes:     dataplane.ClusterVolumes{Dynamic: cluster.Dynamic, Now: now},
		Preferences: dataplane.IdentityPreferences{Service: accounts.Service},
		Roots:       dataplane.DirectoryRoots{Base: files},
		Now:         now,
	})
	if err != nil {
		t.Fatalf("build the data plane: %v", err)
	}
	dataServer := httptest.NewServer(service.Handler())
	t.Cleanup(dataServer.Close)

	return &Case{
		Cluster:      cluster,
		Accounts:     accounts,
		Registry:     registry,
		OwnerToken:   owner,
		Artifacts:    artifacts,
		Installation: installed,
		Files:        files,
		Now:          now,
		host:         host,
		registry:     applications,
		server:       dataServer,
	}
}

// ProductArtifact returns the artifact the SDK packs for this product, together
// with its verified package. The cases install these bytes rather than a
// hand-written declaration, so the artifact under test is the one an author
// ships (KOSD-C-17, SDK-C-4).
func ProductArtifact(t *testing.T) (packaging.Package, []byte) {
	t.Helper()
	pkg, archive, err := product.Build(product.DefaultEntryOrigin)
	if err != nil {
		t.Fatalf("build the product package: %v", err)
	}
	return pkg, archive
}

// InstallArtifact installs one package artifact as a system component and runs
// one install pass: the single install path every extension uses, first party or
// third party (KOSD-C-17). A digest the artifact does not match is refused by
// that same pass.
func InstallArtifact(t *testing.T, cluster *harness.Cluster, artifacts extension.MemoryArtifacts, name, digest string, content []byte, sink extension.PreferenceRetention) *extension.Installation {
	t.Helper()
	reference := "test://" + name
	artifacts[reference] = content
	cluster.Create(t, &unstructured.Unstructured{Object: harness.Component(name, reference, digest)})
	return cluster.ExtensionStateWith(t, artifacts, sink)
}

// LoadRegistry loads the window host's view of the applications the install pass
// activated. It reports exactly what the control plane reports — the extension,
// component, phase, digest and manifest — so the window host judges the installed
// bytes rather than a declaration this repository wrote for it (KOSD-C-73).
func LoadRegistry(t *testing.T, installation *extension.Installation) *apphost.Registry {
	t.Helper()
	installed := installation.Registry().Apps()
	contributions := make([]apphost.Contribution, 0, len(installed))
	for _, application := range installed {
		raw, err := json.Marshal(application.Manifest)
		if err != nil {
			t.Fatalf("encode the manifest of application %s: %v", application.Manifest.ID, err)
		}
		contributions = append(contributions, apphost.Contribution{
			Extension: application.Extension,
			Component: application.Component,
			Phase:     application.Phase,
			Digest:    application.Digest,
			Manifest:  raw,
		})
	}
	registry := apphost.Load(contributions, apphost.ValidationOptions{})
	if apps := registry.Apps(); len(apps) != 1 {
		t.Fatalf("the window host loaded %d applications, want one: refusals %+v", len(apps), registry.Refusals())
	}
	return registry
}

// Applications reads the control plane's application projection for one session
// (KOSD-C-18, KOSD-C-73). The list is what the desktop and the window host read,
// so an install or uninstall is observable there.
func (c *Case) Applications(t *testing.T, sessionToken string) []map[string]any {
	t.Helper()
	status, body := c.Accounts.HTTP.Call(t, http.MethodGet, "/api/v1/apps", nil, c.session(sessionToken))
	if status != http.StatusOK {
		t.Fatalf("read the application projection = %d %v", status, body)
	}
	items, _ := body["items"].([]any)
	applications := make([]map[string]any, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("application item is not an object: %v", item)
		}
		applications = append(applications, record)
	}
	return applications
}

// Account creates one further account with a role and returns a usable
// control-plane session.
func (c *Case) Account(t *testing.T, username, password string, role write.Role) string {
	t.Helper()
	status, body := c.Accounts.HTTP.Call(t, http.MethodPost, "/api/v1/accounts",
		map[string]any{"username": username, "role": string(role)}, c.session(c.OwnerToken))
	if status != http.StatusCreated {
		t.Fatalf("create account %s = %d %v", username, status, body)
	}
	oneTime, _ := body["oneTimePassword"].(string)
	if oneTime == "" {
		t.Fatalf("create account %s returned no one-time password: %v", username, body)
	}
	token, _ := c.Accounts.SignIn(t, username, oneTime)
	c.Accounts.ChangePassword(t, token, oneTime, password)
	token, _ = c.Accounts.SignIn(t, username, password)
	return token
}

// SetPreference writes one declared preference of the signed-in account in this
// product's domain (DASH-C-27).
func (c *Case) SetPreference(t *testing.T, sessionToken, key, value string) {
	t.Helper()
	c.SetPreferenceIn(t, sessionToken, product.PreferenceDomain, key, value)
}

// SetPreferenceIn writes one declared preference in an explicit domain, so a
// case can keep a peer product's data alongside this product's.
func (c *Case) SetPreferenceIn(t *testing.T, sessionToken, domain, key, value string) {
	t.Helper()
	status, body := c.Accounts.HTTP.Call(t, http.MethodPut,
		"/api/v1/preferences/"+domain+"/"+key,
		map[string]any{"value": value}, c.session(sessionToken))
	if status != http.StatusNoContent {
		t.Fatalf("write preference %s/%s = %d %v", domain, key, status, body)
	}
}

// Preferences reads every declared preference of the signed-in account.
func (c *Case) Preferences(t *testing.T, sessionToken string) map[string]string {
	t.Helper()
	status, body := c.Accounts.HTTP.Call(t, http.MethodGet,
		"/api/v1/preferences/"+product.PreferenceDomain, nil, c.session(sessionToken))
	if status != http.StatusOK {
		t.Fatalf("read preferences = %d %v", status, body)
	}
	items, _ := body["items"].(map[string]any)
	values := make(map[string]string, len(items))
	for key, value := range items {
		if text, ok := value.(string); ok {
			values[key] = text
		}
	}
	return values
}

// CreateVolume writes one volume with an owner and, when phase is not empty, the
// status the volume reconciler would have written (KOSD-C-38). The data plane
// only reflects the stored status; the transitions themselves belong to the
// volume reconciler.
func (c *Case) CreateVolume(t *testing.T, name, owner, phase string) {
	t.Helper()
	volume := harness.Volume(harness.Namespace, name, map[string]any{
		"owner":    owner,
		"mobility": string(v1beta1.VolumeMobilityNodeLocal),
		"capacity": "1Gi",
	})
	c.Cluster.Create(t, volume)
	if phase != "" {
		c.SetVolumeStatus(t, name, phase, "")
	}
}

// SetVolumeStatus writes one volume's phase, and the bound node when the phase
// names one.
func (c *Case) SetVolumeStatus(t *testing.T, name, phase, node string) {
	t.Helper()
	status := map[string]any{"phase": phase}
	if node != "" {
		status["node"] = node
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.Cluster.ApplyStatus(ctx, "v1beta1", "Volume", harness.Namespace, name, status); err != nil {
		t.Fatalf("set the status of volume %s: %v", name, err)
	}
}

// VolumeDir returns the directory this host serves for one volume, creating it
// on first use (FILE-C-3). A case writes and removes files there, so the data
// plane reads the same directory a mounted volume would present.
func (c *Case) VolumeDir(t *testing.T, namespace, name string) string {
	t.Helper()
	directory := filepath.Join(c.Files, namespace, name)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("create the directory of volume %s/%s: %v", namespace, name, err)
	}
	return directory
}

// TransferVolume moves a volume to another owner through the control plane, as
// the global Owner does (KOSD-C-60).
func (c *Case) TransferVolume(t *testing.T, ownerToken, name, to string) {
	t.Helper()
	body := harness.Manifest("Volume", harness.Namespace, name, nil, nil)
	body["spec"] = map[string]any{"owner": to}
	version := c.Cluster.Get(t, "v1beta1", "Volume", name).GetResourceVersion()
	status, answer := c.Accounts.HTTP.Call(t, http.MethodPut,
		"/api/v1/resources/Volume/"+name+"?namespace="+harness.Namespace, body,
		map[string]string{"If-Match": version, "Authorization": "Bearer " + ownerToken})
	if status != http.StatusAccepted {
		t.Fatalf("transfer volume %s to %s = %d %v", name, to, status, answer)
	}
}

// OpenWindow opens one window instance of this application for one account and
// completes the handshake that delivers the application token (KOSD-C-75).
func (c *Case) OpenWindow(t *testing.T, account string) Window {
	t.Helper()
	window, _, err := c.host.Open(c.registry, conversationOf(account), account, product.AppID)
	if err != nil {
		t.Fatalf("open the window for %s: %v", account, err)
	}
	init, err := c.host.Handshake(c.registry, account, window.ID, window.Origin, window.HandshakeNonce,
		[]byte(`{"type":"ready"}`))
	if err != nil {
		t.Fatalf("handshake the window for %s: %v", account, err)
	}
	if init.Token == "" {
		t.Fatal("the handshake delivered no application token")
	}
	return Window{ID: window.ID, Token: init.Token}
}

// OpenInstance registers a window instance without completing the handshake and
// reports whether the host opened a new one. A single-mode application answers
// the instance the same browser session already holds (DASH-C-74).
func (c *Case) OpenInstance(t *testing.T, account string) (apphost.Window, bool) {
	t.Helper()
	window, opened, err := c.host.Open(c.registry, conversationOf(account), account, product.AppID)
	if err != nil {
		t.Fatalf("open the window instance for %s: %v", account, err)
	}
	return window, opened
}

// conversationOf names the browser session a case opens its windows in.
func conversationOf(account string) string {
	return "conversation-" + account
}

// Application returns the `App` contribution the window host loaded from this
// product's packaged manifest.
func (c *Case) Application(t *testing.T) apphost.Entry {
	t.Helper()
	application, found := c.registry.App(product.AppID)
	if !found {
		t.Fatalf("the window host loaded no application %s", product.AppID)
	}
	return application
}

// CloseWindow closes one window instance, which revokes its token (KOSD-C-75).
func (c *Case) CloseWindow(t *testing.T, account string, window Window) {
	t.Helper()
	if err := c.host.Close(account, window.ID); err != nil {
		t.Fatalf("close the window for %s: %v", account, err)
	}
}

// DataPlane calls the product's data plane with explicit headers.
func (c *Case) DataPlane(t *testing.T, method, path string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	status, _, body := c.DataPlaneResponse(t, method, path, headers)
	return status, body
}

// DataPlaneJSON calls the product's data plane with one JSON body, so a case can
// exercise a change operation rather than a read (FILE-C-5).
func (c *Case) DataPlaneJSON(t *testing.T, method, path string, payload any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode the body of %s %s: %v", method, path, err)
	}
	carried := make(map[string]string, len(headers)+1)
	for name, value := range headers {
		carried[name] = value
	}
	carried["Content-Type"] = "application/json"
	return c.DataPlaneSend(t, method, path, carried, string(encoded))
}

// DataPlaneResponse calls the product's data plane and reports the answer's
// headers beside its decoded body, so a case can assert what a refusal must not
// carry, such as a download disposition.
func (c *Case) DataPlaneResponse(t *testing.T, method, path string, headers map[string]string) (int, http.Header, map[string]any) {
	t.Helper()
	status, answerHeaders, content := c.DataPlaneRaw(t, method, path, headers, "")
	body := map[string]any{}
	if err := json.Unmarshal(content, &body); err != nil {
		t.Fatalf("decode the data-plane answer of %s %s: %v", method, path, err)
	}
	return status, answerHeaders, body
}

// DataPlaneSend calls the product's data plane with a request body and reports
// the answer decoded, which is what an upload session sends and receives.
func (c *Case) DataPlaneSend(t *testing.T, method, path string, headers map[string]string, body string) (int, map[string]any) {
	t.Helper()
	status, _, content := c.DataPlaneRaw(t, method, path, headers, body)
	answer := map[string]any{}
	if err := json.Unmarshal(content, &answer); err != nil {
		t.Fatalf("decode the data-plane answer of %s %s: %v", method, path, err)
	}
	return status, answer
}

// DataPlaneRaw calls the product's data plane and reports the answer's status,
// headers and bytes, so a case can compare a streamed download with its file.
func (c *Case) DataPlaneRaw(t *testing.T, method, path string, headers map[string]string, body string) (int, http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, c.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build the data-plane request: %v", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("call the data plane: %v", err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the data-plane answer of %s %s: %v", method, path, err)
	}
	return response.StatusCode, response.Header, content
}

// Bearer is the credential header a window's application token travels in.
func Bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// ConsoleCookie is the console session's cookie, sent alone. The data plane
// must never accept it as an identity (KOSD-C-75).
func ConsoleCookie(session string) map[string]string {
	return map[string]string{"Cookie": "kos_session=" + session}
}

// session is the control-plane credential header.
func (c *Case) session(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// ThirdPartyDeclaration is a peer package with the same contribution class, so
// the install cases can tell this product's withdrawal from a framework one.
func ThirdPartyDeclaration() extension.Declaration {
	return extension.Declaration{
		Identifier:       "reporting.example",
		Version:          "1.0.0",
		FrameworkVersion: ">=1.0 <2.0",
		Contributions: extension.Contributions{
			Preferences: []extension.PreferenceSchema{{
				ID:   "reporting.example",
				Keys: []string{"range"},
			}},
		},
	}
}

// ApplyRuntime writes the deployment document this product ships: the
// Application instance and the data-plane entry whose target application is that
// instance (KOSD-C-45).
func (c *Case) ApplyRuntime(t *testing.T, runtime product.Runtime) {
	t.Helper()
	c.Cluster.Create(t, &unstructured.Unstructured{Object: runtime.Application})
	c.Cluster.Create(t, &unstructured.Unstructured{Object: runtime.Endpoint})
}

// EntryEndpoints reads the endpoints whose target application is appID through
// the control plane's reverse lookup (KOSD-C-45).
func (c *Case) EntryEndpoints(t *testing.T, sessionToken, appID string) []*unstructured.Unstructured {
	t.Helper()
	status, body := c.Accounts.HTTP.Call(t, http.MethodGet,
		"/api/v1/resources/Endpoint?targetApp="+appID+"&namespace="+harness.Namespace,
		nil, c.session(sessionToken))
	if status != http.StatusOK {
		t.Fatalf("read the entries of %s = %d %v", appID, status, body)
	}
	items, _ := body["items"].([]any)
	endpoints := make([]*unstructured.Unstructured, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		object, ok := record["object"].(map[string]any)
		if !ok {
			continue
		}
		endpoints = append(endpoints, &unstructured.Unstructured{Object: object})
	}
	return endpoints
}

// VolumeEntries reads the volume list of one window answer.
func VolumeEntries(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["volumes"].([]any)
	if !ok {
		t.Fatalf("window carries no volume list: %v", body)
	}
	entries := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("volume entry is not an object: %v", item)
		}
		entries = append(entries, entry)
	}
	return entries
}

// VolumeNames reads the volume names of one window answer, in answer order.
func VolumeNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	names := make([]string, 0)
	for _, entry := range VolumeEntries(t, body) {
		name, _ := entry["name"].(string)
		names = append(names, name)
	}
	return names
}

// ErrorCode reads the refusal code of one answer.
func ErrorCode(t *testing.T, body map[string]any) string {
	t.Helper()
	failure, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("answer carries no error envelope: %v", body)
	}
	code, _ := failure["code"].(string)
	return code
}

// ErrorMessage reads the refusal message of one answer.
func ErrorMessage(t *testing.T, body map[string]any) string {
	t.Helper()
	failure, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("answer carries no error envelope: %v", body)
	}
	message, _ := failure["message"].(string)
	return message
}
