//go:build !e2e_attacker

package mesh

// e2eAttacker is false in every release build: the F-03 test hooks are only
// compiled with -tags e2e_attacker. See attacker_e2e.go.
const e2eAttacker = false
