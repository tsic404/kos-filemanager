// Package dataplane is the file manager's own service (NG10): it lists the
// volumes a window's account may open and reports the status each volume's
// projection carries (FILE-C-1, FILE-C-2).
//
// Every request is authorized by the window instance's application token
// (KOSD-C-75); the console session cookie is never read, so it cannot act as
// an identity here. The service keeps no index and re-reads its facts on every
// request, so a volume that recovers is usable on the next browse without
// reopening the window.
package dataplane

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tsic404/kosd/api/v1beta1"

	"github.com/tsic404/kos-filemanager/pkg/product"
)

// Errors the data plane answers with. They name refusals of this service, not
// of the control plane.
const (
	// CodeUnauthenticated refuses a request that carries no application token.
	CodeUnauthenticated = "unauthenticated"
	// CodeTokenInvalid refuses a token that expired, was revoked, or belongs to
	// a window that is gone (KOSD-C-75).
	CodeTokenInvalid = "token_invalid"
	// CodeTokenForeignApp refuses a token minted for another application.
	CodeTokenForeignApp = "token_foreign_app"
	// CodeVolumeNotFound refuses a volume the cluster does not hold.
	CodeVolumeNotFound = "volume_not_found"
	// CodeVolumeUnauthorized refuses a volume another account owns (FILE-C-1).
	CodeVolumeUnauthorized = "volume_unauthorized"
	// CodeVolumeUnavailable refuses an operation on a volume whose projection is
	// not Bound (FILE-C-2).
	CodeVolumeUnavailable = "volume_unavailable"
	// CodeVolumesUnreadable refuses a request whose volume read failed; it is
	// never answered with an empty list (KOSD-C-13).
	CodeVolumesUnreadable = "volumes_unreadable"
)

// Binding is what one application token authorizes (KOSD-C-75): the account,
// the application, the window instance and the token's expiry.
type Binding struct {
	// AppID is the application the token belongs to.
	AppID string
	// Account is the account that opened the window.
	Account string
	// WindowID is the window instance the token is bound to.
	WindowID string
	// ExpiresAt is the moment the token stops being usable.
	ExpiresAt time.Time
}

// Authenticator resolves one application token. The window host that minted the
// token is its only issuer, so it is also the only verifier (KOSD-C-75).
type Authenticator interface {
	Authenticate(value string) (Binding, error)
}

// ObservedVolume is one volume read from its source and the moment the source
// observed it (KOSD-C-13).
type ObservedVolume struct {
	// Volume is the volume object as stored.
	Volume v1beta1.Volume
	// ObservedAt is when the source observed this object.
	ObservedAt time.Time
}

// VolumeSource reads the volumes the data plane serves. It returns every volume
// it can see; the service decides which ones the account may open.
type VolumeSource interface {
	// List returns the volumes the source can observe.
	List(ctx context.Context) ([]ObservedVolume, error)
	// Get returns one volume; ok is false when the cluster does not hold it.
	Get(ctx context.Context, namespace, name string) (ObservedVolume, bool, error)
}

// PreferenceSource reads the account's file-manager preferences (DASH-C-27).
type PreferenceSource interface {
	// Preferences returns the declared keys that hold a value.
	Preferences(ctx context.Context, account string) (map[string]string, error)
}

// Config wires the data plane to its facts.
type Config struct {
	// Tokens verifies the application tokens every request carries; required.
	Tokens Authenticator
	// Volumes reads the volume objects; required.
	Volumes VolumeSource
	// Preferences reads the account's preferences; nil serves the defaults.
	Preferences PreferenceSource
	// Log records a failure the caller must not learn about; nil discards it.
	Log *slog.Logger
	// Now supplies the response clock; defaults to time.Now.
	Now func() time.Time
}

// Service is the file manager's data plane.
type Service struct {
	tokens      Authenticator
	volumes     VolumeSource
	preferences PreferenceSource
	log         *slog.Logger
	now         func() time.Time
}

// New builds the data plane.
func New(config Config) (*Service, error) {
	if config.Tokens == nil {
		return nil, errors.New("dataplane: an application token verifier is required")
	}
	if config.Volumes == nil {
		return nil, errors.New("dataplane: a volume source is required")
	}
	service := &Service{
		tokens:      config.Tokens,
		volumes:     config.Volumes,
		preferences: config.Preferences,
		log:         config.Log,
		now:         config.Now,
	}
	if service.log == nil {
		service.log = slog.New(slog.DiscardHandler)
	}
	if service.now == nil {
		service.now = time.Now
	}
	return service, nil
}

// Window is what a window instance opens with: the account's view preferences
// and the volumes it may actually open (FILE-C-1, DASH-C-27).
type Window struct {
	// View is the browser view, list or grid.
	View string `json:"view"`
	// InitialPath is the path the window opens at.
	InitialPath string `json:"initialPath"`
	// Volumes are the volumes this account owns, each with its projection state.
	Volumes []ListedVolume `json:"volumes"`
	// Problems lists non-fatal reads that fell back to a default.
	Problems []string `json:"problems,omitempty"`
}

// Handler serves the data plane. The paths stay outside /api/v1 because file
// access is not a control-plane resource API (NG10).
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+product.HealthPath, s.handleHealth)
	mux.Handle("GET /files/window", s.authorize(http.HandlerFunc(s.handleWindow)))
	mux.Handle("GET /files/volumes/{namespace}/{name}", s.authorize(http.HandlerFunc(s.handleVolume)))
	return mux
}

type bindingKey struct{}

// authorize resolves the application token and puts the binding on the request.
// A request without one is refused: the console session cookie is not an
// identity of this service (KOSD-C-75).
func (s *Service) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value, carried := bearerToken(r.Header.Get("Authorization"))
		if !carried {
			writeFailure(w, http.StatusUnauthorized, CodeUnauthenticated,
				"数据面只接受窗口实例的应用令牌，请重新打开窗口")
			return
		}
		binding, err := s.tokens.Authenticate(value)
		if err != nil {
			writeFailure(w, http.StatusUnauthorized, CodeTokenInvalid,
				"应用令牌已失效，请重新打开窗口")
			return
		}
		if binding.AppID != product.AppID {
			writeFailure(w, http.StatusForbidden, CodeTokenForeignApp,
				"应用令牌不属于本应用")
			return
		}
		if !binding.ExpiresAt.IsZero() && !s.now().Before(binding.ExpiresAt) {
			writeFailure(w, http.StatusUnauthorized, CodeTokenInvalid,
				"应用令牌已失效，请重新打开窗口")
			return
		}
		ctx := context.WithValue(r.Context(), bindingKey{}, binding)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// handleHealth answers the health address the manifest declares (KOSD-C-73).
func (s *Service) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"application": product.AppID,
		"observedAt":  s.now().UTC(),
	})
}

// handleWindow opens one window instance: the declared preferences plus the
// volume list scoped to the token's account.
func (s *Service) handleWindow(w http.ResponseWriter, r *http.Request) {
	binding := bindingOf(r.Context())
	window := Window{View: product.DefaultView, Volumes: []ListedVolume{}}
	values, err := s.accountPreferences(r.Context(), binding.Account)
	if err != nil {
		s.log.Warn("read the file manager preferences", "account", binding.Account, "error", err)
		window.Problems = append(window.Problems, "偏好读取失败，已使用默认视图")
	} else {
		if stored := values[product.PreferenceView]; stored == product.ViewList || stored == product.ViewGrid {
			window.View = stored
		}
		window.InitialPath = values[product.PreferenceInitialPath]
	}
	observed, err := s.volumes.List(r.Context())
	if err != nil {
		s.log.Warn("read the volumes", "account", binding.Account, "error", err)
		writeFailure(w, http.StatusServiceUnavailable, CodeVolumesUnreadable,
			"读取卷失败，请稍后重试")
		return
	}
	now := s.now()
	for _, candidate := range observed {
		if listed, authorized := s.describe(candidate, binding.Account, now); authorized {
			window.Volumes = append(window.Volumes, listed)
		}
	}
	sort.Slice(window.Volumes, func(left, right int) bool {
		if window.Volumes[left].Namespace != window.Volumes[right].Namespace {
			return window.Volumes[left].Namespace < window.Volumes[right].Namespace
		}
		return window.Volumes[left].Name < window.Volumes[right].Name
	})
	writeJSON(w, http.StatusOK, window)
}

// handleVolume answers the state of one volume before any later browse handler
// touches its files. A volume that is not Bound is refused with the reason the
// projection carries (FILE-C-2).
func (s *Service) handleVolume(w http.ResponseWriter, r *http.Request) {
	binding := bindingOf(r.Context())
	namespace, name := r.PathValue("namespace"), r.PathValue("name")
	observed, found, err := s.volumes.Get(r.Context(), namespace, name)
	if err != nil {
		s.log.Warn("read the volume", "volume", namespace+"/"+name, "error", err)
		writeFailure(w, http.StatusServiceUnavailable, CodeVolumesUnreadable,
			"读取卷失败，请稍后重试")
		return
	}
	if !found {
		writeFailure(w, http.StatusNotFound, CodeVolumeNotFound, "卷 "+namespace+"/"+name+" 不存在")
		return
	}
	listed, authorized := s.describe(observed, binding.Account, s.now())
	if !authorized {
		writeFailure(w, http.StatusForbidden, CodeVolumeUnauthorized,
			"卷 "+namespace+"/"+name+" 不在本账号的授权范围")
		return
	}
	if !listed.CanBrowse {
		writeBody(w, http.StatusConflict, map[string]any{
			"error":  map[string]any{"code": CodeVolumeUnavailable, "message": listed.Reason},
			"volume": listed,
		})
		return
	}
	writeJSON(w, http.StatusOK, listed)
}

// accountPreferences reads the declared keys; an unwired source answers the
// defaults instead of failing the window.
func (s *Service) accountPreferences(ctx context.Context, account string) (map[string]string, error) {
	if s.preferences == nil {
		return map[string]string{}, nil
	}
	return s.preferences.Preferences(ctx, account)
}

// bearerToken reads one Bearer credential. A request that carries anything
// else in the header is treated as carrying no token.
func bearerToken(header string) (string, bool) {
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	if value == "" || strings.ContainsAny(value, " \t") {
		return "", false
	}
	return value, true
}

// bindingOf returns the resolved token binding of one request.
func bindingOf(ctx context.Context) Binding {
	binding, _ := ctx.Value(bindingKey{}).(Binding)
	return binding
}

// writeFailure answers one refusal in the envelope the console renders.
func writeFailure(w http.ResponseWriter, status int, code, message string) {
	writeBody(w, status, map[string]any{
		"error": map[string]any{"code": code, "message": message},
	})
}

// writeJSON answers one success.
func writeJSON(w http.ResponseWriter, status int, body any) {
	writeBody(w, status, body)
}

// writeBody writes one encoded answer.
func writeBody(w http.ResponseWriter, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		http.Error(w, "encode the answer", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(encoded, '\n'))
}
