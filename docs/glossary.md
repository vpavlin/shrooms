# Glossary

The words this project uses, each with where it is defined. The definition
there wins over the line here. `scripts/check-agent-docs.py` checks in CI
that every path, ADR and package symbol below (such as `agent.LocalAddr`) still exists.

## The mesh

- **Mesh**: a person's devices joined to each other over WireGuard. A device
  can be in several, each under a label of its own (`home`, `office`).
  `internal/mesh`; ADR-001.
- **Overlay address**: a device's address inside a mesh, derived from its
  keys, with nothing to hand out or collide. `internal/identity`; ADR-005.
  Every peer also gets a synthetic IPv4 address: `internal/v4`, ADR-021.
- **Rendezvous**: how devices find each other: signed announces on rotating
  Logos Delivery (Waku) content topics. `internal/rendezvous`,
  `internal/topic`; ADR-003, ADR-006.
- **Announce**: a device's signed message on the rendezvous topic saying
  where it can be reached. `internal/control`.
- **Disco**: small authenticated probes over the WireGuard socket that find
  which of a peer's endpoints actually works. `internal/disco`; ADR-029.
- **Relay**: forwards WireGuard packets between peers that cannot reach each
  other. `internal/relay`, `cmd/shrooms-relay`.
- **Invite**: a one-time token that enrols a device in a mesh.
  `internal/invite`; ADR-017.
- **Admin key**: the authority over who is a member, separate from taking
  part (`internal/cred`); it can live on a Keycard (ADR-022).
- **Core / Edge**: Logos Delivery node modes. Phones run Edge
  (`state.ModeEdge`); ADR-035.
- **LAN host**: one device that cannot run shrooms (a printer, a Home
  Assistant box), offered on listed TCP ports by a member on its LAN, its
  gateway, under the mesh admin's grant. Named `<host>.lan.<gateway>.<mesh>.mesh`.
  `internal/lanhost`, `internal/lanproxy`, `cred.LanGrant`; ADR-068.
- **LAN alias**: a member's own mapping of a LAN address to a LAN host
  (`mesh.<label>.lan_aliases`), for applications that dial the device's LAN
  address. Routed into the mesh only while the host is offered and its
  gateway is online.
  `lanhost.Alias`; ADR-068.
- **Mesh names**: `laptop.office.mesh`, device then mesh label then suffix,
  answered by the daemon's own DNS (`internal/dns`, `mesh.QualifiedDNSName`).
  Always qualified by the mesh; there is no short form. The suffix is `.mesh`
  unless `hosts_suffix` says otherwise. ADR-032 chose `.internal`
  (`dns.DefaultSuffix`), with `.mesh` (`dns.LegacySuffix`) still answered;
  the config's default has not followed yet.
- **Last-good config**: `config.toml.last-good`, the config the daemon last
  started cleanly on. When an edit is refused it runs on that copy and
  `shrooms status` opens with `CONFIG REFUSED`. ADR-080.

## Shrooms Agents

- **Agent**: `cmd/shrooms-agent`, one per machine and user, serving that
  user's sessions on port `agent.Port` of the mesh addresses and on its own
  socket. ADR-036.
- **Session**: one coding-agent conversation the agent runs, addressed as
  MACHINE/SESSION. `internal/agent`.
- **Harness**: the program a session runs: Claude Code, pi or OpenCode.
  `internal/agent/harness.go`; ADR-054, ADR-061. A harness comes with every
  feature a session has, caged and sealed included, or the missing one is
  refused by name (ADR-062).
- **Task**: a message one agent sends another, open until the worker
  finishes it. ADR-041, ADR-042.
- **Not caged, caged, sealed**: the three ways a session runs, one
  sentence each in `docs/agents/cages.md`. Not caged: your agent, with your
  machine. Caged: your agent, kept in its folder. Sealed: for code or
  content you do not trust.
- **Cage**: a rootless podman container a session runs in, reaching other
  agents only through its own. `internal/agent/cage.go`; ADR-037, ADR-044.
  A harness says how it is caged by implementing `agent.Cageable` (ADR-062).
- **Private harness home**: a caged session's own `~/.claude` (or
  `~/.pi/agent`, or OpenCode's directories), with the owner's settings
  read-only and only its own conversations. ADR-060.
- **Model gateway**: the agent holds the model keys; a cage reaches its
  model through its own agent, which adds them on the way out, so no key is
  ever in a cage. `internal/agent/gateway.go`, `agent.GatewayListen`;
  ADR-059.
- **Sealed cage**: a cage for code nobody vouches for: the internet and
  nothing local, it only finishes its own tasks, and its results leave
  through an outbox. `internal/agent/sealed.go`; ADR-045;
  `docs/agents-files.md`.
- **Files between agents**: a session can push a file to another that takes
  files from it. ADR-048.
- **Published file**: a file a session hands out as a link, through the MCP
  tool `publish_file`: uploaded to a server named by the agent's
  `--files-url`, or kept by the agent itself and served on its mesh
  addresses. The whole mesh can read it; a sealed session cannot publish.
  `internal/agent/publish.go`; ADR-079.
- **Schedule**: a session's routines, kept in a Scala calendar of its own
  that the owner and that session co-own. `internal/agent/schedule.go`;
  ADR-064.
- **Skills and responsibilities**: on a session's card, what it is good at
  (A2A skills) and what it may do without asking; set by the owner only.
  `internal/agent/roles.go`; ADR-063.
- **No mesh**: the agent on one machine alone, on its socket
  (`agent.SocketPath`), this machine standing in as `agent.LocalAddr`.
  ADR-056.

## Around it

- **Basecamp**: Logos's desktop app shell. This repository's modules are
  `basecamp/` (Shrooms), `basecamp/core` (`shrooms_core`) and
  `basecamp-agents/` (Shrooms Agents). ADR-025, ADR-050.
- **Logos Delivery**: the messaging network (Waku) the mesh finds peers on,
  through liblogosdelivery. `internal/waku`.
