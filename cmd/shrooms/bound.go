package main

import (
	"flag"
	"fmt"
	"net/netip"
	"os"
	"text/tabwriter"

	"github.com/vpavlin/shrooms/internal/listeners"
	"github.com/vpavlin/shrooms/internal/mesh"
)

// cmdBound shows what would be announced if announce_bound were on.
//
// A disclosure setting deserves a way to see what it would disclose *before*
// switching it on. These ports are found rather than declared (ADR-026), so
// the honest question — "what exactly would my peers be told?" — cannot be
// answered by reading the config, which is the whole difference between this
// and `services`.
func cmdBound(args []string) error {
	fs := flag.NewFlagSet("bound", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket of the local daemon")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := fetchStatus(*sock)
	if err != nil {
		return err
	}

	// This device's address on each mesh. Only these count: a socket on ::
	// is reachable from every network this machine is on, and listing it as
	// mesh-only would be false.
	type addressed struct {
		label string
		addr  netip.Addr
	}
	var mine []addressed
	if len(st.Meshes) > 0 {
		for _, m := range st.Meshes {
			if a, err := netip.ParseAddr(m.Overlay); err == nil {
				mine = append(mine, addressed{m.Label, a})
			}
		}
	} else if a, err := netip.ParseAddr(st.Overlay); err == nil {
		mine = append(mine, addressed{"", a})
	}
	if len(mine) == 0 {
		return fmt.Errorf("no mesh address to look at")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "MESH\tWOULD ANNOUNCE\tREACHED AS")
	found := 0
	for _, m := range mine {
		ls, err := listeners.On([]netip.Addr{m.addr})
		if err != nil {
			return err
		}
		for _, l := range ls {
			// The same exclusions the announcement makes, or this would
			// promise something it will not do.
			if l.Port == 80 || l.Port == 443 || l.Port == 53 {
				continue
			}
			// The name a peer would type — which on a node with more than
			// one mesh has to carry the label.
			//
			// It did not, and the short form is answered by the primary mesh
			// alone: every row for a second mesh named an address on the
			// first one, where nothing is listening. A column headed "reached
			// as" that cannot be reached is worse than no column, and it took
			// binding something to a second mesh to see it.
			// Each part sanitised on its own and then joined: sanitising
			// "laptop.test" as one string turns the separator into a hyphen
			// and yields laptop-test.mesh, which is a different name and
			// resolves to nothing at all.
			name := mesh.DNSName(st.Name, "")
			if len(mine) > 1 && m.label != "" {
				if host, label := mesh.SanitiseName(st.Name), mesh.SanitiseName(m.label); host != "" && label != "" {
					name = host + "." + label + ".mesh"
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s:%d\n", m.label, l.Spec(), name, l.Port)
			found++
		}
	}
	w.Flush()

	if found == 0 {
		fmt.Println("\nNothing is bound to a mesh address.")
		fmt.Println("A service reachable only over the mesh is one bound to that address —")
		fmt.Println("sshd on the mesh address rather than on ::, for instance.")
		return nil
	}
	fmt.Printf("\n%d would be announced with announce_bound = \"true\".\n", found)
	fmt.Println("They are already reachable by every member; this is about being told.")
	return nil
}
