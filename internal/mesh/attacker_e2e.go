//go:build e2e_attacker

package mesh

// e2eAttacker enables the audit F-03 test hooks: a daemon built with
// -tags e2e_attacker may announce a chosen WireGuard public key
// (SHROOMS_ANNOUNCE_WG) and, for the remembered-state scenario, accept a
// duplicate tunnel key for one run (SHROOMS_E2E_ALLOW_DUPLICATE_WG).
//
// A release build does not include this file; the constant is false there
// (attacker_off.go), so neither variable has any effect. The hooks exist only
// so the e2e scripts can play a member announcing another device's key.
const e2eAttacker = true
