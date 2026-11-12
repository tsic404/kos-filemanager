package dataplane

import (
	"context"
	"fmt"

	"github.com/tsic404/kosd/pkg/identity"

	"github.com/tsic404/kos-filemanager/pkg/product"
)

// IdentityPreferences reads the account's preferences from the control plane's
// preference store (DASH-C-27). Only declared keys are returned, and a key the
// declaration does not name is ignored rather than served.
type IdentityPreferences struct {
	// Service is the account service that owns the preference domain; required.
	Service *identity.Service
	// Domain is the preference domain; empty names this product's domain.
	Domain string
}

// Preferences returns the declared keys that hold a value for one account.
func (p IdentityPreferences) Preferences(ctx context.Context, account string) (map[string]string, error) {
	if p.Service == nil {
		return nil, fmt.Errorf("dataplane: no preference store is configured")
	}
	domain := p.Domain
	if domain == "" {
		domain = product.PreferenceDomain
	}
	return p.Service.Preferences(ctx, identity.Session{Username: account}, domain)
}
