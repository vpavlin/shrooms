// Command shrooms is the mesh daemon and CLI.
//
//	shrooms init [--name N]        create a mesh: keys, authority, credential
//	shrooms invite                 admit one device, once
//	shrooms join --invite TOKEN    join a mesh you were invited to
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
	case "invite":
		err = cmdInvite(os.Args[2:])
	case "daemon":
		err = cmdDaemon(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "reload":
		err = cmdReload(os.Args[2:])
	case "paths":
		err = cmdPaths(os.Args[2:])
	case "hosts":
		err = cmdHosts(os.Args[2:])
	case "key":
		err = cmdKey(os.Args[2:])
	case "keys":
		err = cmdKeys(os.Args[2:])
	case "credential":
		err = cmdCredential(os.Args[2:])
	case "admin":
		err = cmdAdmin(os.Args[2:])
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
  shrooms init [--name N] [--relay]     create a mesh: network key, admin keys
                                          and this device's own credential
  shrooms invite [--name N]             admit one device, once, within 15 min
  shrooms join --invite TOKEN           join the mesh that invite came from
  shrooms join KEY [--name N]           join with the network key itself
  shrooms version                       print the build this binary came from
  shrooms prepare [--name N] [--relay]  write a config with the key left blank,
                                          for setting a machine up without the
                                          key passing through anyone else
  shrooms keys                            this device's public keys, to enrol it
  shrooms credential set BLOB             install a credential issued elsewhere
  shrooms admin init                      mint an authority separately, when
                                            init was run with --no-admin
  shrooms admin issue --name N            sign a credential for this device
  shrooms admin renew                    reissue for everyone near expiry
  shrooms admin revoke --device HEX       withdraw one before it expires
  shrooms admin show                      the mesh id and its trusted keys
  shrooms set-key                       write the key into a prepared config,
                                          read from a prompt or stdin so it
                                          never reaches shell history
  shrooms daemon                     run the mesh node
  shrooms status [--json]            show the roster and tunnel state
  shrooms reload                     re-read the config; applies services
  shrooms paths [NAME]               show probed candidates and which won
  shrooms hosts [--write]            /etc/hosts entries, so you can use names
  shrooms key show                   print the network key
  shrooms key rotate                 replace it (the only revocation before M5)

Common flags:
  --config PATH    config file (default /etc/shrooms/config.toml)
  --state PATH     state directory (default /var/lib/shrooms)

Add a device with `+"`shrooms invite`"+` on a machine that is already a member,
and `+"`shrooms join --invite`"+` on the one that is not. The network key never
appears on a screen, and the new device is issued a credential in the same
exchange.
`)
}
