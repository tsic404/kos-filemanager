package dataplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/tsic404/kosd/api"
	"github.com/tsic404/kosd/api/v1beta1"
)

// volumeResource is the served Volume class (KOSD-C-38).
var volumeResource = schema.GroupVersionResource{
	Group: api.Group, Version: v1beta1.GroupVersion.Version, Resource: "volumes",
}

// ClusterVolumes reads volumes from the cluster the data plane runs against.
// Every read observes the object now, so the reported state is current unless a
// caller wraps it with a different observation time.
type ClusterVolumes struct {
	// Dynamic reads the volume objects; required.
	Dynamic dynamic.Interface
	// Now supplies the observation clock; defaults to time.Now.
	Now func() time.Time
}

// List returns every volume the cluster holds. The service filters by owner.
func (c ClusterVolumes) List(ctx context.Context) ([]ObservedVolume, error) {
	if c.Dynamic == nil {
		return nil, errors.New("dataplane: no cluster client is configured")
	}
	items, err := c.Dynamic.Resource(volumeResource).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	observedAt := c.now()
	observed := make([]ObservedVolume, 0, len(items.Items))
	for index := range items.Items {
		volume, err := decodeVolume(&items.Items[index])
		if err != nil {
			return nil, err
		}
		observed = append(observed, ObservedVolume{Volume: volume, ObservedAt: observedAt})
	}
	return observed, nil
}

// Get returns one volume; ok is false when the cluster does not hold it.
func (c ClusterVolumes) Get(ctx context.Context, namespace, name string) (ObservedVolume, bool, error) {
	if c.Dynamic == nil {
		return ObservedVolume{}, false, errors.New("dataplane: no cluster client is configured")
	}
	object, err := c.Dynamic.Resource(volumeResource).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ObservedVolume{}, false, nil
		}
		return ObservedVolume{}, false, fmt.Errorf("read volume %s/%s: %w", namespace, name, err)
	}
	volume, err := decodeVolume(object)
	if err != nil {
		return ObservedVolume{}, false, err
	}
	return ObservedVolume{Volume: volume, ObservedAt: c.now()}, true, nil
}

// now reads the observation clock.
func (c ClusterVolumes) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

// decodeVolume converts one stored object into the typed volume.
func decodeVolume(object *unstructured.Unstructured) (v1beta1.Volume, error) {
	var volume v1beta1.Volume
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &volume); err != nil {
		return v1beta1.Volume{}, fmt.Errorf("decode volume %s: %w", object.GetName(), err)
	}
	return volume, nil
}
