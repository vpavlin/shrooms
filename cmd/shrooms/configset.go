package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// `shrooms config set <key> <value>` — the settings the daemon already exposes,
// from a terminal.
//
// A thin client over the control socket, not a second implementation. Every one
// of these is an endpoint the Basecamp module and the phone already use, which
// means one place decides what a setting means, one place validates it, and one
// place applies it live. A CLI that edited config.toml itself would be a third
// writer racing the other two, and would silently skip the "apply it now" half
// that makes these settings different from editing the file.
//
// The list is data rather than a switch statement so `config set` with no
// arguments can print it, and so completion has something to read.

// setting is one thing that can be changed over the socket.
type setting struct {
	name string
	// path on the control socket.
	path string
	// value describes what the value looks like, for help and completion.
	value string
	// choices, when there is a fixed set. Empty means free-form.
	choices []string
	// body builds the request from the value the user typed.
	body func(value string) (any, error)
	help string
}

// mesh-scoped settings take `--mesh`, which the daemon reads from the label
// field. Kept out of the table because it changes the shape of the request
// rather than the value in it.
func boolBody(field string) func(string) (any, error) {
	return func(v string) (any, error) {
		b, err := parseOnOff(v)
		if err != nil {
			return nil, err
		}
		return map[string]any{field: b}, nil
	}
}

func parseOnOff(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "yes", "true", "1", "enable", "enabled":
		return true, nil
	case "off", "no", "false", "0", "disable", "disabled":
		return false, nil
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b, nil
	}
	return false, fmt.Errorf("%q is not on or off", v)
}

func settings() []setting {
	return []setting{
		{
			name: "name", path: "/config/name", value: "<name>",
			help: "what this device is called on the mesh",
			body: func(v string) (any, error) {
				if strings.TrimSpace(v) == "" {
					return nil, errors.New("a name cannot be empty")
				}
				return map[string]any{"name": v}, nil
			},
		},
		{
			name: "mode", path: "/config/mode", value: "Core|Edge",
			choices: []string{"Core", "Edge"},
			help:    "Core relays for the network (~20 MB/h); Edge subscribes only (~3 MB/h)",
			body: func(v string) (any, error) {
				switch strings.ToLower(v) {
				case "core":
					return map[string]any{"mode": "Core"}, nil
				case "edge":
					return map[string]any{"mode": "Edge"}, nil
				}
				return nil, fmt.Errorf("%q is neither Core nor Edge", v)
			},
		},
		{
			name: "services", path: "/config/services", value: "<name:port,...>",
			help: "ports published under a name, e.g. immich:2283,jellyfin:8096",
			body: func(v string) (any, error) {
				var list []string
				for _, s := range strings.Split(v, ",") {
					if s = strings.TrimSpace(s); s != "" {
						list = append(list, s)
					}
				}
				return map[string]any{"services": list}, nil
			},
		},
		{
			name: "relay", path: "/config/relay", value: "on|off",
			choices: []string{"on", "off"},
			help:    "forward for peers of this mesh that cannot reach each other",
			body:    boolBody("enabled"),
		},
		{
			name: "portmap", path: "/config/portmap", value: "on|off",
			choices: []string{"on", "off"},
			help:    "ask the router to open this node's port (ADR-024)",
			body:    boolBody("enabled"),
		},
		{
			name: "announce-services", path: "/config/announce", value: "on|off",
			choices: []string{"on", "off"},
			help:    "tell this mesh's peers the names of services published here",
			body:    boolBody("enabled"),
		},
		{
			name: "announce-bound", path: "/config/announce-bound", value: "on|off",
			choices: []string{"on", "off"},
			help:    "tell this mesh's peers which ports are listening on its address",
			body:    boolBody("enabled"),
		},
		{
			name: "mesh", path: "/config/mesh", value: "on|off",
			choices: []string{"on", "off"},
			help:    "run this mesh, or leave it configured and switched off (needs --mesh)",
			body:    boolBody("enabled"),
		},
	}
}

func settingNamed(name string) (setting, bool) {
	for _, s := range settings() {
		if s.name == name {
			return s, true
		}
	}
	return setting{}, false
}

func configSetUsage() {
	fmt.Print("Usage:\n  shrooms config set <setting> <value> [--mesh LABEL]\n\nSettings:\n")
	list := settings()
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
	w := 0
	for _, s := range list {
		if n := len(s.name) + len(s.value); n > w {
			w = n
		}
	}
	for _, s := range list {
		pad := strings.Repeat(" ", w-len(s.name)-len(s.value)+2)
		fmt.Printf("  %s %s%s%s\n", s.name, s.value, pad, s.help)
	}
	fmt.Print(`
Applied by the running daemon, which writes the config and applies what it
can immediately — the same path the desktop module and the phone use. Some
settings say they take effect on the next restart; that is in the answer.

  shrooms config set relay on --mesh home
  shrooms config set services immich:2283,jellyfin:8096
`)
}

func cmdConfigSet(args []string) error {
	// Hand-parsed rather than with flag, because the value can start with a
	// dash — a service list, a name — and flag would take it for a flag.
	var mesh, sock string
	var positional []string
	sock = DefaultSocket
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--mesh":
			if i+1 >= len(args) {
				return errors.New("--mesh needs a label")
			}
			mesh, i = args[i+1], i+1
		case "--socket":
			if i+1 >= len(args) {
				return errors.New("--socket needs a path")
			}
			sock, i = args[i+1], i+1
		case "-h", "--help":
			configSetUsage()
			return nil
		default:
			positional = append(positional, args[i])
		}
	}

	if len(positional) == 0 {
		configSetUsage()
		return errors.New("which setting?")
	}
	s, ok := settingNamed(positional[0])
	if !ok {
		configSetUsage()
		return fmt.Errorf("no setting called %q", positional[0])
	}
	if len(positional) < 2 {
		return fmt.Errorf("%s needs a value (%s)", s.name, s.value)
	}

	body, err := s.body(strings.Join(positional[1:], " "))
	if err != nil {
		return err
	}
	if mesh != "" {
		m, _ := body.(map[string]any)
		m["label"] = mesh
		body = m
	} else if s.name == "mesh" {
		return errors.New("which mesh? pass --mesh LABEL")
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := socketClient(sock, 30*time.Second).
		Post("http://unix"+s.path, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		return fmt.Errorf("no daemon on %s: %w\n\n"+
			"config set goes through the daemon, so it applies the change as well as "+
			"writing it. With the daemon stopped, edit the file and run "+
			"`shrooms config validate`.", sock, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode/100 != 2 {
		return errors.New(strings.TrimSpace(string(out)))
	}
	var answer struct {
		Result string `json:"result"`
	}
	if json.Unmarshal(out, &answer) == nil && answer.Result != "" {
		fmt.Println(answer.Result)
		return nil
	}
	fmt.Print(string(out))
	return nil
}
