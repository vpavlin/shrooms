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

go 1.25.0

require (
	github.com/vpavlin/shrooms v0.0.0
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
)

require (
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/mobile v0.0.0-20260803200217-62cee1672c8e // indirect
	golang.org/x/mod v0.38.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/tools v0.48.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
)

replace github.com/vpavlin/shrooms => ../

tool golang.org/x/mobile/cmd/gobind
