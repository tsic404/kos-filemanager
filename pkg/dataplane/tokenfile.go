package dataplane

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// TokenTable resolves application tokens from the digest table the window host
// publishes. The table never carries a token value, only the digest of one, so
// reading it does not deliver a usable credential (KOSD-C-14).
//
// Every request reads the table again: a window that closes, an account that
// signs out and an application that is uninstalled leave the table on the
// host's next write, and the next request sees it (KOSD-C-75).
type TokenTable struct {
	// Path is the published table; required.
	Path string
	// Now is the expiry clock; defaults to time.Now.
	Now func() time.Time
}

// tokenTableDocument is the file the window host publishes.
type tokenTableDocument struct {
	// Tokens lists one entry per live token.
	Tokens []tokenTableEntry `json:"tokens"`
}

// tokenTableEntry is one live token binding.
type tokenTableEntry struct {
	// Digest is the lowercase hex SHA-256 of the token value.
	Digest string `json:"digest"`
	// AppID is the application the token belongs to.
	AppID string `json:"appId"`
	// Account is the account that opened the window.
	Account string `json:"account"`
	// WindowID is the window instance the token is bound to.
	WindowID string `json:"windowId"`
	// ExpiresAt is the moment the token stops being usable.
	ExpiresAt time.Time `json:"expiresAt"`
}

// Authenticate resolves one token value against the current table.
func (t TokenTable) Authenticate(value string) (Binding, error) {
	if t.Path == "" {
		return Binding{}, errors.New("dataplane: no token table is configured")
	}
	raw, err := os.ReadFile(t.Path)
	if err != nil {
		return Binding{}, fmt.Errorf("dataplane: read the token table: %w", err)
	}
	var document tokenTableDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return Binding{}, fmt.Errorf("dataplane: decode the token table: %w", err)
	}
	sum := sha256.Sum256([]byte(value))
	presented := hex.EncodeToString(sum[:])
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	for _, entry := range document.Tokens {
		if subtle.ConstantTimeCompare([]byte(entry.Digest), []byte(presented)) != 1 {
			continue
		}
		if entry.ExpiresAt.IsZero() || !now().Before(entry.ExpiresAt) {
			return Binding{}, errors.New("dataplane: the application token expired")
		}
		return Binding{
			AppID:     entry.AppID,
			Account:   entry.Account,
			WindowID:  entry.WindowID,
			ExpiresAt: entry.ExpiresAt,
		}, nil
	}
	return Binding{}, errors.New("dataplane: the application token is not in the table")
}
