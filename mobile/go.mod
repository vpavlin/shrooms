// A separate module, deliberately.
//
// `gomobile bind` requires golang.org/x/mobile in the module holding the bound
// package, and adding it to the root module bumped the go directive to 1.25 and
// upgraded x/sys, x/sync and x/tools — changing wireguard-go's dependency graph
// as a side effect of wanting an .aar. The core's dependencies are the ones its
// tests ran against, and the Android toolchain should not get to move them.
//
// Nested modules are excluded from the parent's ./... patterns automatically,
// so the root build and test are untouched.
module github.com/vpavlin/shrooms/mobile

go 1.26.0

toolchain go1.26.10

// No multipath TCP on our listeners (ADR-075): nothing here uses it, and
// CVE-2026-98432's published root exploit went through an MPTCP listener.
godebug multipathtcp=0

require (
	github.com/ethereum/go-ethereum v1.17.0
	github.com/vpavlin/shrooms v0.0.0
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
)

require (
	github.com/ProjectZKM/Ziren/crates/go-runtime/zkvm_runtime v0.0.0-20251001021608-1fe7b43fc4d6 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/holiman/uint256 v1.3.2 // indirect
	github.com/keycard-tech/keycard-go/v4 v4.0.1 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/mobile v0.0.0-20260803200217-62cee1672c8e // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.9.0 // indirect
	golang.org/x/tools v0.51.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c // indirect
)

replace github.com/vpavlin/shrooms => ../

tool golang.org/x/mobile/cmd/gobind
