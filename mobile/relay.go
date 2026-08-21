package mobile

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/vpavlin/shrooms/internal/state"
)

// Pointing a phone at a relay somebody else runs (docs/blind-relays.md).
//
// This is the setting a phone most needs and can least discover. A phone on
// mobile data is behind carrier-grade NAT, which means it cannot be dialled at
// all — no amount of hole punching reaches it, because the carrier rewrites the
// port per destination. A relay is the only way its traffic moves, and a blind
// relay cannot announce itself, so somebody has to type it in.

// BlindRelays returns the configured blind relays, comma-separated for the UI.
func BlindRelays(configDir string) string {
	cfg, _, err := load(configDir)
	if err != nil {
		return ""
	}
	return strings.Join(cfg.RelayBlind, ", ")
}

// BlindRelayToken returns the configured token, empty when the relays are open.
func BlindRelayToken(configDir string) string {
	cfg, _, err := load(configDir)
	if err != nil {
		return ""
	}
	return cfg.RelayToken
}

// SetBlindRelays replaces the list, accepting the commas and spaces a person
// typing on a phone will produce.
//
// Addresses are validated here rather than at connect. A mistyped relay is
// otherwise indistinguishable from one that is down — both are silence — and
// the difference matters because only one of them is worth waiting for.
func SetBlindRelays(configDir, list, token string) error {
	cfg, _, err := load(configDir)
	if err != nil {
		return err
	}

	var out []string
	for _, one := range strings.FieldsFunc(list, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\t'
	}) {
		if _, err := netip.ParseAddrPort(one); err != nil {
			return fmt.Errorf("%q is not an address and port, like 203.0.113.10:31760", one)
		}
		out = append(out, one)
	}
	cfg.RelayBlind = out
	cfg.RelayToken = strings.TrimSpace(token)

	cfgPath, _ := paths(configDir)
	return state.WriteConfig(cfgPath, cfg)
}
