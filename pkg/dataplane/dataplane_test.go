package dataplane_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tsic404/kosd/api/v1beta1"
	volumeapi "github.com/tsic404/kosd/pkg/volume"

	"github.com/tsic404/kos-filemanager/pkg/dataplane"
	"github.com/tsic404/kos-filemanager/pkg/product"
)

// clock is the instant every case reads on, so a stale observation is written
// as an exact offset rather than a race with the wall clock.
var clock = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

const (
	account = "alice"
	foreign = "bob"
)

// tokens is a token registry that answers exactly the values it holds.
type tokens map[string]dataplane.Binding

func (t tokens) Authenticate(value string) (dataplane.Binding, error) {
	binding, ok := t[value]
	if !ok {
		return dataplane.Binding{}, errors.New("application token is expired or revoked")
	}
	return binding, nil
}

// volumes is a volume source over a fixed set of observations.
type volumes struct {
	observed []dataplane.ObservedVolume
	listErr  error
}

func (v *volumes) List(context.Context) ([]dataplane.ObservedVolume, error) {
	if v.listErr != nil {
		return nil, v.listErr
	}
	return v.observed, nil
}

func (v *volumes) Get(_ context.Context, namespace, name string) (dataplane.ObservedVolume, bool, error) {
	for _, candidate := range v.observed {
		if candidate.Volume.Namespace == namespace && candidate.Volume.Name == name {
			return candidate, true, nil
		}
	}
	return dataplane.ObservedVolume{}, false, nil
}

// preferences is a preference source answering fixed values or a failure.
type preferences struct {
	values map[string]string
	err    error
}

func (p preferences) Preferences(context.Context, string) (map[string]string, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.values, nil
}

// dataPlane is one running service with its credential table.
type dataPlane struct {
	t      *testing.T
	server *httptest.Server
	tokens tokens
}

// start builds the service over one volume set and one token table.
func start(t *testing.T, source *volumes, prefs preferences) *dataPlane {
	t.Helper()
	return startConfigured(t, dataplane.Config{Volumes: source, Preferences: prefs})
}

// startConfigured builds the service over one explicit configuration, with the
// shared token table and clock already in place.
func startConfigured(t *testing.T, config dataplane.Config) *dataPlane {
	t.Helper()
	table := tokens{
		"alice-token": {
			AppID: product.AppID, Account: account, WindowID: "window-1",
			ExpiresAt: clock.Add(10 * time.Minute),
		},
	}
	config.Tokens = table
	config.Now = func() time.Time { return clock }
	service, err := dataplane.New(config)
	if err != nil {
		t.Fatalf("build the data plane: %v", err)
	}
	server := httptest.NewServer(service.Handler())
	t.Cleanup(server.Close)
	return &dataPlane{t: t, server: server, tokens: table}
}

// call sends one request and decodes the answer.
func (d *dataPlane) call(method, path string, headers map[string]string) (int, map[string]any) {
	d.t.Helper()
	request, err := http.NewRequest(method, d.server.URL+path, nil)
	if err != nil {
		d.t.Fatalf("build the request: %v", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		d.t.Fatalf("call %s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	body := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		d.t.Fatalf("decode the answer of %s %s: %v", method, path, err)
	}
	return response.StatusCode, body
}

// callWithToken sends one request carrying one application token.
func (d *dataPlane) callWithToken(method, path, token string) (int, map[string]any) {
	return d.call(method, path, map[string]string{"Authorization": "Bearer " + token})
}

// listed reads the volume list of one window answer.
func listed(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["volumes"].([]any)
	if !ok {
		t.Fatalf("answer carries no volume list: %v", body)
	}
	volumes := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		record, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("volume entry is not an object: %v", item)
		}
		volumes = append(volumes, record)
	}
	return volumes
}

// volumeNames lists the volume names in answer order.
func volumeNames(t *testing.T, body map[string]any) []string {
	t.Helper()
	names := make([]string, 0)
	for _, volume := range listed(t, body) {
		name, _ := volume["name"].(string)
		names = append(names, name)
	}
	return names
}

// errorCode reads the failure code of one answer.
func errorCode(t *testing.T, body map[string]any) string {
	t.Helper()
	failure, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("answer carries no error envelope: %v", body)
	}
	code, _ := failure["code"].(string)
	return code
}

// volume builds one stored volume with a phase and an observation time.
func volume(name, owner, phase, node string, observedAt time.Time) dataplane.ObservedVolume {
	health := v1beta1.HealthStateHealthy
	return dataplane.ObservedVolume{
		Volume: v1beta1.Volume{
			ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: name},
			Spec:       v1beta1.VolumeSpec{Owner: owner},
			Status: v1beta1.VolumeStatus{
				ObjectStatus: v1beta1.ObjectStatus{
					Phase:  phase,
					Health: v1beta1.Health{State: health},
				},
				Node: node,
			},
		},
		ObservedAt: observedAt,
	}
}

// TestOnlyAnApplicationTokenIsAnIdentity walks the credential shapes the data
// plane must refuse: no credential, the console session cookie, and a malformed
// header (KOSD-C-75).
func TestOnlyAnApplicationTokenIsAnIdentity(t *testing.T) {
	source := &volumes{}
	plane := start(t, source, preferences{})

	testCases := []struct {
		name    string
		headers map[string]string
	}{
		{name: "no credential", headers: nil},
		{name: "console session cookie", headers: map[string]string{"Cookie": "kos_session=console-session"}},
		{name: "bearer without value", headers: map[string]string{"Authorization": "Bearer"}},
		{name: "another scheme", headers: map[string]string{"Authorization": "Basic YWxpY2U6c2VjcmV0"}},
		{name: "two credentials", headers: map[string]string{"Authorization": "Bearer alice-token extra"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			status, body := plane.call(http.MethodGet, "/files/window", testCase.headers)
			if status != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401: %v", status, body)
			}
			if code := errorCode(t, body); code != dataplane.CodeUnauthenticated {
				t.Fatalf("code = %q, want %q", code, dataplane.CodeUnauthenticated)
			}
		})
	}
}

// TestRevokedAndForeignTokensAreRefused checks the two token refusals that are
// not about carriage: a token the host no longer holds (closed window) and one
// minted for another application (KOSD-C-75).
func TestRevokedAndForeignTokensAreRefused(t *testing.T) {
	source := &volumes{}
	plane := start(t, source, preferences{})

	if status, body := plane.callWithToken(http.MethodGet, "/files/window", "not-a-token"); status != http.StatusUnauthorized {
		t.Fatalf("unknown token = %d %v, want 401", status, body)
	} else if code := errorCode(t, body); code != dataplane.CodeTokenInvalid {
		t.Fatalf("code = %q, want %q", code, dataplane.CodeTokenInvalid)
	}

	plane.tokens["foreign-token"] = dataplane.Binding{
		AppID: "another-app", Account: account, WindowID: "window-2", ExpiresAt: clock.Add(time.Minute),
	}
	status, body := plane.callWithToken(http.MethodGet, "/files/window", "foreign-token")
	if status != http.StatusForbidden {
		t.Fatalf("foreign application token = %d %v, want 403", status, body)
	}
	if code := errorCode(t, body); code != dataplane.CodeTokenForeignApp {
		t.Fatalf("code = %q, want %q", code, dataplane.CodeTokenForeignApp)
	}

	// Closing the window removes the binding the host held: the same value is
	// now refused.
	delete(plane.tokens, "alice-token")
	if status, body := plane.callWithToken(http.MethodGet, "/files/window", "alice-token"); status != http.StatusUnauthorized {
		t.Fatalf("revoked token = %d %v, want 401", status, body)
	} else if code := errorCode(t, body); code != dataplane.CodeTokenInvalid {
		t.Fatalf("code = %q, want %q", code, dataplane.CodeTokenInvalid)
	}
}

// TestExpiredBindingIsRefused checks the expiry the service enforces itself, so
// a verifier that handed back a stale binding cannot widen the window.
func TestExpiredBindingIsRefused(t *testing.T) {
	plane := start(t, &volumes{}, preferences{})
	plane.tokens["expired-token"] = dataplane.Binding{
		AppID: product.AppID, Account: account, WindowID: "window-3", ExpiresAt: clock.Add(-time.Second),
	}
	status, body := plane.callWithToken(http.MethodGet, "/files/window", "expired-token")
	if status != http.StatusUnauthorized {
		t.Fatalf("expired token = %d %v, want 401", status, body)
	}
	if code := errorCode(t, body); code != dataplane.CodeTokenInvalid {
		t.Fatalf("code = %q, want %q", code, dataplane.CodeTokenInvalid)
	}
}

// TestWindowListsOnlyOwnedVolumesWithTheirStatus checks FILE-C-1 and FILE-C-2
// on one page: the account's own volumes appear with the projection's phase,
// another account's volume does not appear at all, and an unavailable volume
// carries a readable reason.
func TestWindowListsOnlyOwnedVolumesWithTheirStatus(t *testing.T) {
	source := &volumes{observed: []dataplane.ObservedVolume{
		volume("bound", account, volumeapi.PhaseBound, "node-a", clock),
		volume("pending", account, volumeapi.PhasePending, "", clock),
		volume("lost", account, volumeapi.PhaseLost, "node-b", clock),
		volume("foreign", foreign, volumeapi.PhaseBound, "", clock),
		volume("unobserved", account, "", "", clock),
	}}
	plane := start(t, source, preferences{})

	status, body := plane.callWithToken(http.MethodGet, "/files/window", "alice-token")
	if status != http.StatusOK {
		t.Fatalf("window = %d %v, want 200", status, body)
	}
	names := volumeNames(t, body)
	if len(names) != 4 {
		t.Fatalf("listed volumes = %v, want the four owned volumes and no foreign one", names)
	}
	for _, name := range names {
		if name == "foreign" {
			t.Fatalf("listed volumes = %v, want another account's volume excluded", names)
		}
	}
	byName := make(map[string]map[string]any, len(names))
	for _, entry := range listed(t, body) {
		byName[entry["name"].(string)] = entry
	}
	if canBrowse, _ := byName["bound"]["canBrowse"].(bool); !canBrowse {
		t.Fatalf("bound volume entry = %v, want it browsable", byName["bound"])
	}
	for _, name := range []string{"pending", "lost", "unobserved"} {
		if canBrowse, _ := byName[name]["canBrowse"].(bool); canBrowse {
			t.Fatalf("%s volume entry = %v, want it refused", name, byName[name])
		}
		if reason, _ := byName[name]["reason"].(string); reason == "" {
			t.Fatalf("%s volume entry carries no reason: %v", name, byName[name])
		}
	}
	if phase, _ := byName["lost"]["phase"].(string); phase != volumeapi.PhaseLost {
		t.Fatalf("lost volume phase = %q, want %q", phase, volumeapi.PhaseLost)
	}
}

// TestStaleObservationIsMarkedAndUnknown checks the freshness bound: an
// observation past two minutes is marked stale and its health is Unknown, while
// the stored status is not rewritten (KOSD-C-13).
func TestStaleObservationIsMarkedAndUnknown(t *testing.T) {
	source := &volumes{observed: []dataplane.ObservedVolume{
		volume("old", account, volumeapi.PhaseBound, "", clock.Add(-3*time.Minute)),
		volume("fresh", account, volumeapi.PhaseBound, "", clock),
	}}
	plane := start(t, source, preferences{})

	_, body := plane.callWithToken(http.MethodGet, "/files/window", "alice-token")
	byName := map[string]map[string]any{}
	for _, entry := range listed(t, body) {
		byName[entry["name"].(string)] = entry
	}
	if stale, _ := byName["old"]["stale"].(bool); !stale {
		t.Fatalf("stale entry = %v, want the stale marker", byName["old"])
	}
	if health, _ := byName["old"]["health"].(string); health != string(v1beta1.HealthStateUnknown) {
		t.Fatalf("stale health = %q, want %q", health, v1beta1.HealthStateUnknown)
	}
	if stale, _ := byName["fresh"]["stale"].(bool); stale {
		t.Fatalf("fresh entry = %v, want no stale marker", byName["fresh"])
	}
}

// TestVolumeRefusals checks the single-volume entry: absence, another owner's
// volume, and each unavailable phase answer their own refusal, while a Bound
// volume answers its state (FILE-C-1, FILE-C-2).
func TestVolumeRefusals(t *testing.T) {
	source := &volumes{observed: []dataplane.ObservedVolume{
		volume("bound", account, volumeapi.PhaseBound, "node-a", clock),
		volume("pending", account, volumeapi.PhasePending, "", clock),
		volume("lost", account, volumeapi.PhaseLost, "node-b", clock),
		volume("foreign", foreign, volumeapi.PhaseBound, "", clock),
	}}
	plane := start(t, source, preferences{})

	if status, body := plane.callWithToken(http.MethodGet, "/files/volumes/team/missing", "alice-token"); status != http.StatusNotFound {
		t.Fatalf("missing volume = %d %v, want 404", status, body)
	} else if code := errorCode(t, body); code != dataplane.CodeVolumeNotFound {
		t.Fatalf("code = %q, want %q", code, dataplane.CodeVolumeNotFound)
	}

	if status, body := plane.callWithToken(http.MethodGet, "/files/volumes/team/foreign", "alice-token"); status != http.StatusForbidden {
		t.Fatalf("another owner's volume = %d %v, want 403", status, body)
	} else if code := errorCode(t, body); code != dataplane.CodeVolumeUnauthorized {
		t.Fatalf("code = %q, want %q", code, dataplane.CodeVolumeUnauthorized)
	}

	for _, name := range []string{"pending", "lost"} {
		status, body := plane.callWithToken(http.MethodGet, "/files/volumes/team/"+name, "alice-token")
		if status != http.StatusConflict {
			t.Fatalf("%s volume = %d %v, want 409", name, status, body)
		}
		if code := errorCode(t, body); code != dataplane.CodeVolumeUnavailable {
			t.Fatalf("code = %q, want %q", code, dataplane.CodeVolumeUnavailable)
		}
		if reason, _ := body["error"].(map[string]any)["message"].(string); reason == "" {
			t.Fatalf("%s volume carries no readable reason: %v", name, body)
		}
	}

	if status, body := plane.callWithToken(http.MethodGet, "/files/volumes/team/bound", "alice-token"); status != http.StatusOK {
		t.Fatalf("bound volume = %d %v, want 200", status, body)
	}
}

// TestRecoveredVolumeIsUsableOnTheNextRequest checks FILE-C-2's recovery
// clause: the same window and token see the new phase without reopening.
func TestRecoveredVolumeIsUsableOnTheNextRequest(t *testing.T) {
	source := &volumes{observed: []dataplane.ObservedVolume{
		volume("recovering", account, volumeapi.PhasePending, "", clock),
	}}
	plane := start(t, source, preferences{})

	if status, body := plane.callWithToken(http.MethodGet, "/files/volumes/team/recovering", "alice-token"); status != http.StatusConflict {
		t.Fatalf("pending volume = %d %v, want 409", status, body)
	}
	source.observed[0].Volume.Status.Phase = volumeapi.PhaseBound
	source.observed[0].Volume.Status.Node = "node-a"
	if status, body := plane.callWithToken(http.MethodGet, "/files/volumes/team/recovering", "alice-token"); status != http.StatusOK {
		t.Fatalf("recovered volume = %d %v, want 200 on the next request", status, body)
	}
}

// TestWindowServesDeclaredPreferences checks the view and initial path the
// window opens with, and the fallback for a stored value outside the vocabulary
// (DASH-C-27).
func TestWindowServesDeclaredPreferences(t *testing.T) {
	plane := start(t, &volumes{}, preferences{values: map[string]string{
		product.PreferenceView:        product.ViewGrid,
		product.PreferenceInitialPath: "/documents",
	}})
	status, body := plane.callWithToken(http.MethodGet, "/files/window", "alice-token")
	if status != http.StatusOK {
		t.Fatalf("window = %d %v, want 200", status, body)
	}
	if view, _ := body["view"].(string); view != product.ViewGrid {
		t.Fatalf("view = %q, want %q", view, product.ViewGrid)
	}
	if path, _ := body["initialPath"].(string); path != "/documents" {
		t.Fatalf("initialPath = %q, want /documents", path)
	}

	unknown := start(t, &volumes{}, preferences{values: map[string]string{
		product.PreferenceView: "carousel",
	}})
	_, body = unknown.callWithToken(http.MethodGet, "/files/window", "alice-token")
	if view, _ := body["view"].(string); view != product.DefaultView {
		t.Fatalf("view = %q, want the default %q", view, product.DefaultView)
	}
}

// TestPreferenceFailureDoesNotHideTheVolumes checks the degraded path: the
// volume list still serves when the preference store cannot be read, and the
// window says the view fell back to its default.
func TestPreferenceFailureDoesNotHideTheVolumes(t *testing.T) {
	source := &volumes{observed: []dataplane.ObservedVolume{
		volume("bound", account, volumeapi.PhaseBound, "", clock),
	}}
	plane := start(t, source, preferences{err: errors.New("database unavailable")})
	status, body := plane.callWithToken(http.MethodGet, "/files/window", "alice-token")
	if status != http.StatusOK {
		t.Fatalf("window = %d %v, want 200", status, body)
	}
	if names := volumeNames(t, body); len(names) != 1 {
		t.Fatalf("listed volumes = %v, want the volume despite the preference failure", names)
	}
	problems, _ := body["problems"].([]any)
	if len(problems) == 0 {
		t.Fatalf("window carries no problem for the failed preference read: %v", body)
	}
}

// TestVolumeReadFailureIsNotAnEmptyList checks KOSD-C-13's error semantics: a
// failed read is reported, never answered as an empty window.
func TestVolumeReadFailureIsNotAnEmptyList(t *testing.T) {
	plane := start(t, &volumes{listErr: errors.New("cluster unreachable")}, preferences{})
	status, body := plane.callWithToken(http.MethodGet, "/files/window", "alice-token")
	if status != http.StatusServiceUnavailable {
		t.Fatalf("failed read = %d %v, want 503", status, body)
	}
	if code := errorCode(t, body); code != dataplane.CodeVolumesUnreadable {
		t.Fatalf("code = %q, want %q", code, dataplane.CodeVolumesUnreadable)
	}
}

// TestHealthNeedsNoCredential checks the address the manifest declares: the
// console probes it without an application session (KOSD-C-73).
func TestHealthNeedsNoCredential(t *testing.T) {
	plane := start(t, &volumes{}, preferences{})
	status, body := plane.call(http.MethodGet, product.HealthPath, nil)
	if status != http.StatusOK {
		t.Fatalf("health = %d %v, want 200", status, body)
	}
	if application, _ := body["application"].(string); application != product.AppID {
		t.Fatalf("health names application %q, want %q", application, product.AppID)
	}
}
