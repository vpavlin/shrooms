package state

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/vpavlin/shrooms/internal/identity"
)

// InviteScheme is the URI a QR code carries.
//
// A URI rather than a bare key so the phone can tell a mesh invitation from any
// other text it might scan, and so a name hint can ride along. A bare key is
// still accepted when pasted, because people will paste bare keys.
const InviteScheme = "shrooms"

// LegacyInviteScheme is what these said before the rename. Read, never written
// — see invite.LegacyScheme for the reasoning.
const LegacyInviteScheme = "logosvpn"

// InviteURI renders a network key as something scannable.
func InviteURI(networkKey, meshName string) string {
	v := url.Values{}
	v.Set("key", networkKey)
	if meshName != "" {
		v.Set("mesh", meshName)
	}
	return InviteScheme + "://join?" + v.Encode()
}

// ParseInvite accepts either an invite URI or a bare network key, and returns
// the key and any mesh name hint.
func ParseInvite(s string) (key, meshName string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("empty invitation")
	}

	if !strings.HasPrefix(s, InviteScheme+"://") && !strings.HasPrefix(s, LegacyInviteScheme+"://") {
		// A bare key. Validate it here rather than letting it fail later as
		// "config invalid", which says nothing about what was actually typed.
		if _, err := identity.ParseNetworkKey(s); err != nil {
			return "", "", fmt.Errorf("not a network key or invitation: %w", err)
		}
		return s, "", nil
	}

	u, err := url.Parse(s)
	if err != nil {
		return "", "", fmt.Errorf("malformed invitation: %w", err)
	}
	key = u.Query().Get("key")
	if key == "" {
		return "", "", fmt.Errorf("invitation carries no key")
	}
	if _, err := identity.ParseNetworkKey(key); err != nil {
		return "", "", fmt.Errorf("invitation key: %w", err)
	}
	return key, u.Query().Get("mesh"), nil
}
