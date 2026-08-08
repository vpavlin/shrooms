package main

import (
	"fmt"
	"strings"

	"rsc.io/qr"

	"github.com/vpavlin/logos-vpn/internal/state"
)

// renderQR draws a QR code with half-block characters, two rows per line.
//
// Terminal cells are about twice as tall as they are wide, so one character per
// module produces a rectangle most scanners refuse. Half-blocks give square
// modules at half the height, which fits on a normal terminal.
//
// Rendered dark-on-light regardless of the terminal theme: scanners expect dark
// modules on a light background, and inverting it stops many of them working.
func renderQR(text string) (string, error) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return "", fmt.Errorf("encode qr: %w", err)
	}

	const quiet = 2 // the standard quiet zone; without it scanners struggle
	size := code.Size

	dark := func(x, y int) bool {
		if x < 0 || y < 0 || x >= size || y >= size {
			return false
		}
		return code.Black(x, y)
	}

	var b strings.Builder
	for y := -quiet; y < size+quiet; y += 2 {
		for x := -quiet; x < size+quiet; x++ {
			top, bottom := dark(x, y), dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString(" ")
			case top:
				b.WriteString("▄") // lower half block
			case bottom:
				b.WriteString("▀") // upper half block
			default:
				b.WriteString("█") // full block
			}
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// showKeyQR prints the network key as a scannable code.
func showKeyQR(cfg state.Config) error {
	uri := state.InviteURI(cfg.NetworkKey, cfg.Name)
	art, err := renderQR(uri)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Print(art)
	fmt.Println()
	fmt.Printf("  %s\n\n", cfg.NetworkKey)
	// Said plainly, because a QR code on a screen is a credential in the room.
	// Until M5 replaces this with one-time invites, the only mitigation is not
	// leaving it up.
	fmt.Println("  Scan this from the logos-vpn app to join the mesh.")
	fmt.Println("  It is the mesh key, not a one-time invite: anyone who")
	fmt.Println("  photographs it is a member until the key is rotated.")
	fmt.Println()
	return nil
}
