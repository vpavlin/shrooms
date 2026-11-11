module github.com/vpavlin/shrooms

go 1.26.0

toolchain go1.26.10

// No multipath TCP on our listeners (ADR-075): nothing here uses it, and
// CVE-2026-98432's published root exploit went through an MPTCP listener.
godebug multipathtcp=0

require (
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1
	github.com/keycard-tech/keycard-go/v4 v4.0.1
	golang.org/x/crypto v0.58.0
	golang.org/x/net v0.61.0
	golang.org/x/sys v0.49.0
	golang.org/x/term v0.47.0
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c
	rsc.io/qr v0.2.0
)

require (
	github.com/ProjectZKM/Ziren/crates/go-runtime/zkvm_runtime v0.0.0-20251001021608-1fe7b43fc4d6 // indirect
	github.com/ethereum/go-ethereum v1.17.0 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/holiman/uint256 v1.3.2 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.9.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
)
