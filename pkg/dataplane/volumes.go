package dataplane

import (
	"time"

	"github.com/tsic404/kosd/api/v1beta1"
	"github.com/tsic404/kosd/pkg/controlplane"
	volumeapi "github.com/tsic404/kosd/pkg/volume"
)

// ListedVolume is one volume as the window reports it. The state is the
// projection's, not a value this service infers: phase from the volume's
// status, freshness from the observation time, and a readable reason whenever
// the volume cannot be browsed (FILE-C-2, KOSD-C-38, KOSD-C-13).
type ListedVolume struct {
	// Namespace is the volume's namespace.
	Namespace string `json:"namespace"`
	// Name is the volume's name.
	Name string `json:"name"`
	// Owner is the account the volume belongs to (KOSD-C-60).
	Owner string `json:"owner,omitempty"`
	// Phase is Pending, Bound or Lost; empty means no reconciler wrote one yet.
	Phase string `json:"phase"`
	// Health is the projection's health state; a stale observation is Unknown.
	Health string `json:"health,omitempty"`
	// ObservedAt is when the volume was observed (KOSD-C-13).
	ObservedAt time.Time `json:"observedAt"`
	// Stale marks an observation older than the shared freshness bound.
	Stale bool `json:"stale"`
	// CanBrowse reports whether the volume's files may be opened now.
	CanBrowse bool `json:"canBrowse"`
	// Reason explains a refused browse; empty when CanBrowse is true.
	Reason string `json:"reason,omitempty"`
}

// describe turns one observed volume into the window's entry, and reports
// whether the account may see it at all. Only the volume's owner may see it:
// an exposed-volume preference never widens this (FILE-C-1, KOSD-C-60).
func (s *Service) describe(observed ObservedVolume, account string, now time.Time) (ListedVolume, bool) {
	volume := observed.Volume
	if volume.Spec.Owner == "" || volume.Spec.Owner != account {
		return ListedVolume{}, false
	}
	observedAt := observed.ObservedAt.UTC()
	stale := now.Sub(observedAt) > controlplane.StaleThreshold
	health := string(volume.Status.Health.State)
	if stale {
		// An observation past the bound is not trusted as a current state
		// (KOSD-C-13); the stored status is never rewritten (KOSD-C-38).
		health = string(v1beta1.HealthStateUnknown)
	}
	listed := ListedVolume{
		Namespace:  volume.Namespace,
		Name:       volume.Name,
		Owner:      volume.Spec.Owner,
		Phase:      volume.Status.Phase,
		Health:     health,
		ObservedAt: observedAt,
		Stale:      stale,
	}
	listed.Reason = unavailableReason(volume)
	listed.CanBrowse = listed.Reason == ""
	return listed, true
}

// unavailableReason names why a volume cannot be browsed, from its phase
// (FILE-C-2, KOSD-C-38). An unknown phase is treated as not yet observable
// rather than as usable.
func unavailableReason(volume v1beta1.Volume) string {
	switch volume.Status.Phase {
	case volumeapi.PhaseBound:
		return ""
	case volumeapi.PhasePending:
		return "卷尚未绑定：绑定完成后即可浏览"
	case volumeapi.PhaseLost:
		if volume.Status.Node != "" {
			return "卷已丢失：绑定节点 " + volume.Status.Node + " 失联超过确认窗口"
		}
		return "卷已丢失：卷数据所在节点失联超过确认窗口"
	default:
		return "卷状态尚未观测到，暂不可浏览"
	}
}
