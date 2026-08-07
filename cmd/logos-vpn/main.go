// Command logos-vpn is the mesh daemon and CLI.
//
//	logos-vpn init [--name N]        generate identity and a new network
//	logos-vpn join <KEY> [--name N]  generate identity, join an existing network
//	logos-vpn daemon                 run the mesh
//	logos-vpn status                 roster and tunnel state
//	logos-vpn key show               print the network key
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(os.Args[2:])
	case "join":
		err = cmdJoin(os.Args[2:])
	case "daemon":
		err = cmdDaemon(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "paths":
		err = cmdPaths(os.Args[2:])
	case "hosts":
		err = cmdHosts(os.Args[2:])
	case "key":
		err = cmdKey(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `logos-vpn — overlay mesh over Logos Delivery

Usage:
  logos-vpn init [--name N] [--relay]  create a new mesh, print its network key
  logos-vpn join KEY [--name N] [--relay]  join an existing mesh
  logos-vpn daemon                     run the mesh node
  logos-vpn status [--json]            show the roster and tunnel state
  logos-vpn paths [NAME]               show probed candidates and which won
  logos-vpn hosts [--write]            /etc/hosts entries, so you can use names
  logos-vpn key show                   print the network key
  logos-vpn key rotate                 replace it (the only revocation before M5)

Common flags:
  --config PATH    config file (default /etc/logos-vpn/config.toml)
  --state PATH     state directory (default /var/lib/logos-vpn)

The network key is the only secret. Copy it to your other machines with
`+"`logos-vpn join`"+`.
`)
}
