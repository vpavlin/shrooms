// Command shrooms is the mesh daemon and CLI.
//
//	shrooms init [--name N]        generate identity and a new network
//	shrooms join <KEY> [--name N]  generate identity, join an existing network
//	shrooms daemon                 run the mesh
//	shrooms status                 roster and tunnel state
//	shrooms key show               print the network key
package main

import (
	"fmt"
	"os"
)

// version is set at build time with -X main.version. "dev" means someone built
// this without the Makefile, which is worth being able to tell.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(os.Args[2:])
	case "prepare":
		err = cmdPrepare(os.Args[2:])
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
	case "set-key":
		err = cmdSetKey(os.Args[2:])
	// Not "-v": the daemon uses it for verbose, so `shrooms -v` expecting
	// more logging printed a version string and exited. One letter, two
	// meanings, and the wrong one is silent.
	case "version", "--version":
		fmt.Println(version)
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
	fmt.Fprint(os.Stderr, `shrooms — overlay mesh over Logos Delivery

Usage:
  shrooms init [--name N] [--relay]  create a new mesh, print its network key
  shrooms join KEY [--name N] [--relay]  join an existing mesh
  shrooms version                       print the build this binary came from
  shrooms prepare [--name N] [--relay]  write a config with the key left blank,
                                          for setting a machine up without the
                                          key passing through anyone else
  shrooms set-key                       write the key into a prepared config,
                                          read from a prompt or stdin so it
                                          never reaches shell history
  shrooms daemon                     run the mesh node
  shrooms status [--json]            show the roster and tunnel state
  shrooms paths [NAME]               show probed candidates and which won
  shrooms hosts [--write]            /etc/hosts entries, so you can use names
  shrooms key show                   print the network key
  shrooms key rotate                 replace it (the only revocation before M5)

Common flags:
  --config PATH    config file (default /etc/shrooms/config.toml)
  --state PATH     state directory (default /var/lib/shrooms)

The network key is the only secret. Copy it to your other machines with
`+"`shrooms join`"+`.
`)
}
