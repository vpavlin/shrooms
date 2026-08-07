package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/vpavlin/logos-vpn/internal/hosts"
)

func cmdHosts(args []string) error {
	fs := flag.NewFlagSet("hosts", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket path")
	suffix := fs.String("suffix", "mesh", "domain suffix; '.internal' is the formally reserved choice")
	write := fs.Bool("write", false, "update "+hosts.DefaultFile+" instead of printing")
	file := fs.String("file", hosts.DefaultFile, "hosts file to update")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := fetchStatus(*sock)
	if err != nil {
		return err
	}

	entries := []hosts.Entry{{Name: st.Name, Addr: st.Overlay}}
	for _, p := range st.Peers {
		entries = append(entries, hosts.Entry{Name: p.Name, Addr: p.Overlay})
	}
	block := hosts.Render(entries, *suffix)
	if !*write {
		fmt.Print(block)
		if len(st.Peers) > 0 {
			fmt.Fprintf(os.Stderr, "\n# to apply: sudo logos-vpn hosts --write\n")
		}
		return nil
	}

	changed, err := hosts.Apply(*file, block)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Printf("%s already up to date\n", *file)
		return nil
	}
	fmt.Printf("updated %s with %d entries\n", *file, len(entries))
	fmt.Printf("\nTo keep it current automatically, set in your config:\n")
	fmt.Printf("  manage_hosts = \"true\"\n")
	return nil
}
