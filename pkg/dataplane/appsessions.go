package dataplane

import (
	"fmt"

	"github.com/tsic404/kos-dashboard/pkg/apphost"
)

// AppHost verifies application tokens against the window host that minted
// them. The host keeps the token digests and revokes them on close, logout and
// uninstall, so a refusal here carries exactly the lifecycle KOSD-C-75 fixes.
//
// The target deployment is a standalone data-plane service with its own cluster
// credentials: the console BFF must not hold cluster credentials (KOSD-C-12), so
// mounting the data plane inside the window host would move those credentials
// into the BFF. This adapter therefore only proves the token lifecycle where the
// host's state is reachable in one process; a standalone deployment needs an
// introduction channel that this revision does not have.
type AppHost struct {
	// Host is the application session host; required.
	Host *apphost.Host
}

// Authenticate resolves one token to its binding.
func (a AppHost) Authenticate(value string) (Binding, error) {
	if a.Host == nil {
		return Binding{}, fmt.Errorf("dataplane: no application host is configured")
	}
	binding, err := a.Host.Authenticate(value)
	if err != nil {
		return Binding{}, err
	}
	return Binding{
		AppID:     binding.AppID,
		Account:   binding.Account,
		WindowID:  binding.WindowID,
		ExpiresAt: binding.ExpiresAt,
	}, nil
}
