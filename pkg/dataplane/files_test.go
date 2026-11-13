package dataplane_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	volumeapi "github.com/tsic404/kosd/pkg/volume"

	"github.com/tsic404/kos-filemanager/pkg/dataplane"
)

// serveVolume creates the directory this data plane serves for one volume and
// writes one file into it.
func serveVolume(t *testing.T, base, namespace, name, file, content string) string {
	t.Helper()
	directory := filepath.Join(base, namespace, name)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("create the volume directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, file), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	return directory
}

// TestStaleVolumeObservationStillBrowsesCurrentContent checks KOSD-C-13's
// clause for this surface: the stale marker belongs to the volume projection,
// and it never turns a browse into a cached or refused read. The listing is read
// now and carries its own observation time.
func TestStaleVolumeObservationStillBrowsesCurrentContent(t *testing.T) {
	base := t.TempDir()
	serveVolume(t, base, "team", "stale", "current.txt", "now")
	source := &volumes{observed: []dataplane.ObservedVolume{
		volume("stale", account, volumeapi.PhaseBound, "node-a", clock.Add(-3*time.Minute)),
	}}
	plane := startConfigured(t, dataplane.Config{
		Volumes: source,
		Roots:   dataplane.DirectoryRoots{Base: base},
	})

	// The window marks the volume's observation stale.
	_, windowBody := plane.callWithToken(http.MethodGet, "/files/window", "alice-token")
	summaries := listed(t, windowBody)
	if len(summaries) != 1 {
		t.Fatalf("window lists %d volumes, want one: %v", len(summaries), windowBody)
	}
	if stale, _ := summaries[0]["stale"].(bool); !stale {
		t.Fatalf("window entry = %v, want its observation marked stale", summaries[0])
	}

	status, body := plane.callWithToken(http.MethodGet, "/files/volumes/team/stale/entries", "alice-token")
	if status != http.StatusOK {
		t.Fatalf("browse of a stale projection = %d %v, want 200", status, body)
	}
	entries, ok := body["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("browse lists %v, want current.txt", body["entries"])
	}
	observedAt, ok := body["observedAt"].(string)
	if !ok {
		t.Fatalf("listing carries no observation time: %v", body)
	}
	parsed, err := time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		t.Fatalf("parse observedAt %q: %v", observedAt, err)
	}
	if !parsed.Equal(clock) {
		t.Fatalf("listing observedAt = %s, want this read's instant %s", parsed, clock)
	}
}

// TestBrowseAndPreviewAreNotCached checks that both reads forbid a cache: the
// window has to see the volume's current content on the next browse (FILE-C-3).
func TestBrowseAndPreviewAreNotCached(t *testing.T) {
	base := t.TempDir()
	serveVolume(t, base, "team", "bound", "notes.txt", "text body")
	source := &volumes{observed: []dataplane.ObservedVolume{
		volume("bound", account, volumeapi.PhaseBound, "node-a", clock),
	}}
	plane := startConfigured(t, dataplane.Config{
		Volumes: source,
		Roots:   dataplane.DirectoryRoots{Base: base},
	})

	for _, route := range []string{
		"/files/volumes/team/bound/entries",
		"/files/volumes/team/bound/preview?path=notes.txt",
	} {
		status, headers, body := callResponse(t, plane, route)
		if status != http.StatusOK {
			t.Fatalf("%s = %d %v, want 200", route, status, body)
		}
		if cache := headers.Get("Cache-Control"); cache != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", route, cache)
		}
	}
}

// callResponse sends one authenticated read and returns the answer with its
// headers decoded.
func callResponse(t *testing.T, plane *dataPlane, route string) (int, http.Header, map[string]any) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, plane.server.URL+route, nil)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer alice-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("call %s: %v", route, err)
	}
	defer response.Body.Close()
	body := map[string]any{}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s: %v", route, err)
	}
	return response.StatusCode, response.Header, body
}
