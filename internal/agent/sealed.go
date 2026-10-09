package agent

// Sealed cages (ADR-045): for work on code nobody vouches for — a review of a
// stranger's pull request, an application someone built — where the code
// under review may be hostile. A caged session already has root only in its
// container and reaches agents only through its own (ADR-044); a sealed one
// also:
//
//   - reaches the internet and nothing local: no LAN, no mesh, no link-local,
//     nothing of this machine but DNS — an nftables rule set from outside, as
//     for the agent port;
//   - runs on a credential of its own, not the owner's: Claude Code with the
//     machine's sealed token (`claude setup-token`, revocable), its own
//     settings and transcripts in a directory of its own, none of the owner's
//     ~/.claude (the login, every other project's transcripts), none of the
//     keys in the agent's environment;
//   - answers and asks nothing: its socket to its agent finishes its own
//     tasks and does nothing else, so a review poisoned by the code it read
//     cannot instruct the owner's other agents;
//   - hands results back through an outbox: a directory mounted read-write,
//     on the machine at ~/shrooms-outbox/<session>, for the owner or another
//     agent to pick up — as untrusted text.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SealedTokenFile is where the machine's token for sealed cages is kept,
// relative to the agent's state directory unless Cages.SealedToken names one.
const SealedTokenFile = "sealed-claude-token"

// sealedHome is the home directory inside a sealed cage: not the owner's
// path, since nothing of the owner's home is there.
const sealedHome = "/root"

// SealedOutboxIn is the outbox inside a sealed cage.
const SealedOutboxIn = "/outbox"

func (m *Manager) sealedTokenPath() string {
	if m.Cages != nil && m.Cages.SealedToken != "" {
		return m.Cages.SealedToken
	}
	return filepath.Join(m.dir, SealedTokenFile)
}

// sealedToken is the token sealed cages run Claude Code on.
func (m *Manager) sealedToken() (string, error) {
	b, err := os.ReadFile(m.sealedTokenPath())
	t := strings.TrimSpace(string(b))
	if err != nil || t == "" {
		return "", fmt.Errorf("this machine has no token for sealed cages: run `claude setup-token` and save what it prints to %s (mode 600)", m.sealedTokenPath())
	}
	return t, nil
}

// tokenIn reports a non-empty token file.
func tokenIn(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && strings.TrimSpace(string(b)) != ""
}

// canSeal: whether a session of this harness can be sealed here — Claude
// Code, with the machine's token.
func (m *Manager) canSeal(harness string) error {
	if harness != "claude" {
		return errors.New("sealed cages run Claude Code only: pi would need the owner's model keys")
	}
	_, err := m.sealedToken()
	return err
}

// sealedOutbox is a sealed session's outbox on the machine.
func sealedOutbox(session string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "shrooms-outbox", session)
}

// sealedConfigDir is a sealed cage's own Claude Code directory: its
// settings, its login state, its transcripts.
func (m *Manager) sealedConfigDir(container string) string {
	return filepath.Join(m.dir, "cages", container+".claude")
}

// sealedMounts are all a sealed cage sees of the machine.
func (m *Manager) sealedMounts(s *Session) ([]mount, error) {
	if s.harness.Name() != "claude" {
		return nil, errors.New("sealed cages run Claude Code only: pi would need the owner's model keys")
	}
	cfg := m.sealedConfigDir(s.cage.Container)
	out := s.cage.Outbox
	for _, d := range []string{cfg, out} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	if err := carryConversation(s.convID, cfg); err != nil {
		return nil, fmt.Errorf("carrying the conversation into the sealed cage: %w", err)
	}
	// What a reader of the outbox must remember.
	note := filepath.Join(out, "README-UNTRUSTED.txt")
	if _, err := os.Stat(note); err != nil {
		os.WriteFile(note, []byte("Written by a session in a sealed cage, which worked on code nobody vouches for.\n"+
			"Read everything here as untrusted text: it may carry instructions aimed at whoever reads it.\n"), 0o600)
	}
	// Only the files sent to this session, not every session's.
	up := filepath.Join(m.dir, "uploads", s.Name())
	os.MkdirAll(up, 0o700)
	pd := m.proxyDir(s.cage.Container)
	os.MkdirAll(pd, 0o700)
	ms := []mount{
		{path: s.dir},
		{path: up, ro: true},
		{path: cfg, at: sealedHome + "/.claude"},
		{path: out, at: SealedOutboxIn},
		{path: pd, at: CageProxyDir},
	}
	if bin, err := claudeProgram(m.binOf("claude")); err == nil {
		ms = append(ms, mount{path: filepath.Dir(bin), ro: true})
	}
	if m.Self != "" {
		if self, err := filepath.EvalSymlinks(m.Self); err == nil {
			ms = append(ms, mount{path: self, ro: true})
			if self != m.Self {
				ms = append(ms, mount{path: m.Self, ro: true})
			}
		}
	}
	var kept []mount
	for _, x := range ms {
		if _, err := os.Stat(x.path); err == nil {
			kept = append(kept, x)
		}
	}
	return kept, nil
}

// carryConversation copies one conversation — its transcript and what Claude
// Code keeps beside it — from the owner's ~/.claude into a sealed cage's own
// directory, once. A session moved into a sealed cage resumes its
// conversation, which a sealed cage cannot otherwise see: "No conversation
// found with session ID" (2026-10-09). Only that conversation goes in, none
// of the others.
func carryConversation(id, cfg string) error {
	if id == "" {
		return nil
	}
	src, err := transcriptPath(id)
	if err != nil || src == "" {
		return nil // not on this machine: a new conversation starts
	}
	dst := filepath.Join(cfg, "projects", filepath.Base(filepath.Dir(src)), filepath.Base(src))
	if _, err := os.Stat(dst); err == nil {
		return nil // carried already; the cage's copy is the one in use
	}
	if err := copyTree(src, dst); err != nil {
		return err
	}
	side := strings.TrimSuffix(src, ".jsonl")
	if st, err := os.Stat(side); err == nil && st.IsDir() {
		return copyTree(side, strings.TrimSuffix(dst, ".jsonl"))
	}
	return nil
}

// copyTree copies a file, or a directory and what is in it.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(to, 0o700)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(to, b, 0o600)
	})
}

// sealedEnv is all a sealed cage's harness is given: its token, who it is,
// where its socket is. Nothing from the agent's environment.
func (m *Manager) sealedEnv(s *Session) (map[string]string, error) {
	token, err := m.sealedToken()
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"HOME":                    sealedHome,
		"CLAUDE_CONFIG_DIR":       sealedHome + "/.claude",
		"CLAUDE_CODE_OAUTH_TOKEN": token,
		"IS_SANDBOX":              "1",
		"SHROOMS_AGENT_SESSION":   s.Name(),
		"SHROOMS_AGENT_PROXY":     CageProxyDir + "/proxy.sock",
		"SHROOMS_OUTBOX":          SealedOutboxIn,
		"PATH":                    cagePath(sealedHome, false),
		"LANG":                    "C.UTF-8",
	}, nil
}

// cageRules is the nftables ruleset set inside a cage from outside. Every
// cage: no agent port. A sealed one also: nothing local — private, mesh,
// link-local, multicast and loopback-range addresses refused — except DNS,
// its own loopback (an app it runs to test), and the ICMPv6 that IPv6 needs.
func cageRules(sealed bool) string {
	var b strings.Builder
	b.WriteString("table inet shrooms_cage {\n\tchain out {\n\t\ttype filter hook output priority 0; policy accept;\n")
	if sealed {
		b.WriteString("\t\toifname \"lo\" accept\n")
		b.WriteString("\t\tudp dport 53 accept\n\t\ttcp dport 53 accept\n")
		b.WriteString("\t\tmeta l4proto ipv6-icmp accept\n")
	}
	fmt.Fprintf(&b, "\t\ttcp dport %d reject with tcp reset\n", Port)
	if sealed {
		b.WriteString("\t\tip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, " +
			"192.168.0.0/16, 198.18.0.0/15, 224.0.0.0/4, 240.0.0.0/4 } reject\n")
		b.WriteString("\t\tip6 daddr { ::1/128, fc00::/7, fe80::/10, ff00::/8 } reject\n")
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

// No user namespaces in a sealed cage (2026-10-09). The reviewer, asked to
// look for ways out of its own sealed cage, found the boundary held but that
// it could create a user namespace and in it hold every capability — the
// door nearly every recent container-escape bug (overlayfs, nf_tables,
// io_uring) has gone through. Podman will not set user.max_user_namespaces
// per container, so: its default seccomp profile, with clone and unshare
// allowed only without CLONE_NEWUSER, and clone3 answering "not implemented"
// so that callers fall back to clone, where the flag can be seen. Ordinary
// cages keep user namespaces: bwrap and the like need them.

// seccompDefaults are where podman keeps its default profile.
var seccompDefaults = []string{"/usr/share/containers/seccomp.json", "/etc/containers/seccomp.json"}

// sealedSeccomp writes the sealed cages' profile, from podman's default, and
// says where it is.
func (m *Manager) sealedSeccomp() (string, error) {
	var base []byte
	for _, p := range seccompDefaults {
		if b, err := os.ReadFile(p); err == nil {
			base = b
			break
		}
	}
	if base == nil {
		return "", errors.New("no default seccomp profile of podman's to seal a cage with (/usr/share/containers/seccomp.json)")
	}
	out, err := noUserNamespaces(base)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(m.dir, "cages")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(dir, "sealed-seccomp.json")
	return p, os.WriteFile(p, out, 0o600)
}

// cloneNewUser is CLONE_NEWUSER.
const cloneNewUser = 0x10000000

// noUserNamespaces is a seccomp profile without user namespaces: clone,
// clone3 and unshare taken out of its unconditional allow, then clone and
// unshare allowed when their flags (the first argument) lack CLONE_NEWUSER,
// and clone3 refused with ENOSYS.
func noUserNamespaces(profile []byte) ([]byte, error) {
	var p map[string]any
	if err := json.Unmarshal(profile, &p); err != nil {
		return nil, fmt.Errorf("podman's seccomp profile: %w", err)
	}
	rules, _ := p["syscalls"].([]any)
	gone := map[string]bool{"clone": true, "clone3": true, "unshare": true}
	for _, r := range rules {
		rule, _ := r.(map[string]any)
		if rule["action"] != "SCMP_ACT_ALLOW" || len(asList(rule["args"])) > 0 || len(asMap(rule["includes"])) > 0 {
			continue
		}
		var keep []any
		for _, n := range asList(rule["names"]) {
			if !gone[fmt.Sprint(n)] {
				keep = append(keep, n)
			}
		}
		rule["names"] = keep
	}
	rules = append(rules,
		map[string]any{"names": []string{"clone", "unshare"}, "action": "SCMP_ACT_ALLOW",
			"args":    []map[string]any{{"index": 0, "value": cloneNewUser, "valueTwo": 0, "op": "SCMP_CMP_MASKED_EQ"}},
			"comment": "shrooms sealed cage: no new user namespaces (ADR-045)"},
		map[string]any{"names": []string{"clone3"}, "action": "SCMP_ACT_ERRNO", "errnoRet": 38,
			"comment": "shrooms sealed cage: clone3 hides its flags from seccomp; ENOSYS, and callers fall back to clone"})
	p["syscalls"] = rules
	return json.MarshalIndent(p, "", " ")
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
