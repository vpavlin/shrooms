package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vpavlin/shrooms/internal/service"
)

// `shrooms services` — list, add and remove published services.
//
// The declaration syntax ("backup:8080->127.0.0.1:9090/tls/type=logos-storage")
// is a good storage format and a poor authoring interface. It was defensible
// while a service was a name and a port; a type flag made it something nobody
// should be asked to remember, and `config set services` replaces the whole
// list at once, so adding one meant retyping the others.
//
// So this reads what is published, changes one entry, and writes the list back.
// It builds declarations through service.Spec.String rather than by pasting
// strings together, which is what keeps it from drifting away from the parser.
func cmdServices(args []string) error {
	if len(args) == 0 {
		return servicesUsage()
	}
	switch args[0] {
	case "list":
		return servicesList(args[1:])
	case "add":
		return servicesAdd(args[1:])
	case "remove", "rm":
		return servicesRemove(args[1:])
	default:
		return servicesUsage()
	}
}

func servicesUsage() error {
	fmt.Fprint(os.Stderr, `usage: shrooms services <list|add|remove>

  shrooms services list
  shrooms services add <name> [--port N] [--to host:port] [--type T] [--tls]
  shrooms services remove <name>

Examples:
  shrooms services add immich --port 2283
  shrooms services add backup --to 127.0.0.1:8080 --type logos-storage
  shrooms services add www --port 443 --to 8080 --tls

A --type says what the service IS, as opposed to what it is called here, so that
another device can ask "does this mesh have a logos-storage?" and get an answer.
It comes from IANA's service names (RFC 6335): at most 15 characters, letters,
digits and hyphens.
`)
	return fmt.Errorf("no such subcommand")
}

// takeName pulls a leading positional argument off the front.
//
// Go's flag package stops parsing at the first non-flag argument, so
// "services add immich --port 2283" — which is the order anybody would type —
// would leave every flag unparsed and the name looking like four arguments.
// Taking it off the front first makes both orders work.
func takeName(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

// current reads the published services from the running daemon.
// current reads what is CONFIGURED, not what is running.
//
// The distinction is the whole reason this reads from /config/services rather
// than from /status. Status reports live services; a service that is switched
// off, failed to bind, or was added since the last reload is absent from it.
// Reading the running list and writing it back would delete every configured
// service that happened not to be up — silently, as the ordinary result of
// adding an unrelated one.
func current(sock, mesh string) ([]service.Spec, string, error) {
	url := "http://unix/config/services"
	if mesh != "" {
		url += "?mesh=" + neturl.QueryEscape(mesh)
	}
	resp, err := socketClient(sock, 15*time.Second).Get(url)
	if err != nil {
		return nil, "", fmt.Errorf("no daemon on %s: %w", sock, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusMethodNotAllowed {
		// A daemon that predates the read endpoint. Said plainly, because
		// "POST a setting" is a true and useless thing to tell somebody who
		// typed `services list`.
		return nil, "", fmt.Errorf("this daemon has no way to report its " +
			"configured services, which means it is older than this command. " +
			"`shrooms services` needs a daemon that can be asked what is " +
			"configured, as opposed to what is running — reading the running " +
			"list and writing it back would delete anything configured and not " +
			"currently up.")
	}
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("the daemon refused it: %s", strings.TrimSpace(string(msg)))
	}
	var body struct {
		Services []string `json:"services"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return nil, "", err
	}

	out := make([]service.Spec, 0, len(body.Services))
	for _, decl := range body.Services {
		sp, err := service.ParseSpec(decl)
		if err != nil {
			// Keep going: one unparseable line must not make the others
			// uneditable, and it is the thing the operator most needs to see.
			fmt.Fprintf(os.Stderr, "warning: ignoring unreadable service %q: %v\n", decl, err)
			continue
		}
		out = append(out, sp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	// The suffix is presentation only, so a daemon that cannot report it is
	// not an error.
	suffix := ""
	if st, err := fetchStatus(sock); err == nil {
		suffix = st.DNS.Suffix
	}
	return out, suffix, nil
}

func servicesList(args []string) error {
	fs := flag.NewFlagSet("services list", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket of the local daemon")
	mesh := fs.String("mesh", "", "which mesh, when this node is on several")
	if err := fs.Parse(args); err != nil {
		return err
	}
	specs, suffix, err := current(*sock, *mesh)
	if err != nil {
		return err
	}
	if len(specs) == 0 {
		fmt.Println("No services published. Add one with `shrooms services add`.")
		return nil
	}
	for _, sp := range specs {
		line := fmt.Sprintf("  %-16s port %-6d -> %s", sp.Name, sp.Port, sp.Target)
		if sp.Type != "" {
			line += "  type " + sp.Type
		}
		if sp.TLS {
			line += "  tls"
		}
		fmt.Println(line)
	}
	if suffix != "" {
		fmt.Printf("\nReached from the mesh as <name>.<this device>.%s\n", suffix)
	}
	return nil
}

func servicesAdd(args []string) error {
	name, args := takeName(args)
	fs := flag.NewFlagSet("services add", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket of the local daemon")
	mesh := fs.String("mesh", "", "which mesh, when this node is on several")
	port := fs.Int("port", 0, "port to publish on the mesh address")
	to := fs.String("to", "", "where to forward, as host:port or a bare port for loopback")
	typ := fs.String("type", "", "what this service is, e.g. logos-storage (RFC 6335 service name)")
	tls := fs.Bool("tls", false, "the service speaks TLS, so the name router may serve it over HTTPS")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	}
	if name == "" {
		return fmt.Errorf("expected one name, as in `shrooms services add immich --port 2283`")
	}

	// Assembled as a declaration and parsed back, so that a name, port or type
	// this daemon would refuse is refused here — with the error naming the
	// thing that is wrong — instead of being written to config and failing on
	// the next reload.
	decl := name
	if *port > 0 {
		decl += ":" + strconv.Itoa(*port)
	}
	if *to != "" {
		decl += "->" + *to
	}
	if *tls {
		decl += "/tls"
	}
	if *typ != "" {
		decl += "/type=" + strings.ToLower(*typ)
	}
	spec, err := service.ParseSpec(decl)
	if err != nil {
		return err
	}

	specs, _, err := current(*sock, *mesh)
	if err != nil {
		return err
	}
	replaced := false
	for i := range specs {
		if specs[i].Name == spec.Name {
			specs[i], replaced = spec, true
		}
	}
	if !replaced {
		specs = append(specs, spec)
	}
	if err := writeServices(*sock, *mesh, specs); err != nil {
		// The daemon parses the declaration again, which is what keeps a CLI
		// from writing something it will not load — and means a daemon older
		// than this CLI refuses a flag it has never heard of, with an error
		// about the part it could not make sense of. Say so, because "is not a
		// port" sends people to look at the port.
		if spec.Type != "" {
			return fmt.Errorf("%w\n\n"+
				"If that error looks like it is about the port rather than the "+
				"type, the daemon is probably older than this command: --type "+
				"needs a daemon that knows the flag. Check `shrooms --version` "+
				"against the running one.", err)
		}
		return err
	}
	verb := "Added"
	if replaced {
		verb = "Updated"
	}
	fmt.Printf("%s %s.\n", verb, spec)
	return nil
}

func servicesRemove(args []string) error {
	name, args := takeName(args)
	fs := flag.NewFlagSet("services remove", flag.ExitOnError)
	sock := fs.String("socket", DefaultSocket, "control socket of the local daemon")
	mesh := fs.String("mesh", "", "which mesh, when this node is on several")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	}
	if name == "" {
		return fmt.Errorf("expected one name, as in `shrooms services remove immich`")
	}

	specs, _, err := current(*sock, *mesh)
	if err != nil {
		return err
	}
	kept := make([]service.Spec, 0, len(specs))
	for _, sp := range specs {
		if sp.Name != name {
			kept = append(kept, sp)
		}
	}
	if len(kept) == len(specs) {
		return fmt.Errorf("no service called %q is published here", name)
	}
	if err := writeServices(*sock, *mesh, kept); err != nil {
		return err
	}
	fmt.Printf("Removed %s.\n", name)
	return nil
}

// writeServices replaces the published list through the same endpoint
// `config set services` uses. One writer, so the two cannot disagree about the
// format or about which mesh they are editing.
func writeServices(sock, mesh string, specs []service.Spec) error {
	decls := make([]string, 0, len(specs))
	for _, sp := range specs {
		decls = append(decls, sp.String())
	}
	body := map[string]any{"services": decls}
	if mesh != "" {
		body["label"] = mesh
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := socketClient(sock, 30*time.Second).
		Post("http://unix/config/services", "application/json", bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("no daemon on %s: %w\n\n"+
			"`services` goes through the daemon, so it applies the change as well "+
			"as writing it. With the daemon stopped, edit the file and run "+
			"`shrooms config validate`.", sock, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("the daemon refused it: %s", strings.TrimSpace(string(msg)))
	}
	return nil
}
