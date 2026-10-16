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
- **`.mesh` names**: `laptop.office.mesh`, answered by the daemon's own DNS
  (`internal/dns`, `mesh.QualifiedDNSName`); ADR-032.

## Shrooms Agents

- **Agent**: `cmd/shrooms-agent`, one per machine and user, serving that
  user's sessions on port `agent.Port` of the mesh addresses and on its own
  socket. ADR-036.
- **Session**: one coding-agent conversation the agent runs, addressed as
  MACHINE/SESSION. `internal/agent`.
- **Harness**: the program a session runs: Claude Code or pi.
  `internal/agent/harness.go`; ADR-054.
- **Task**: a message one agent sends another, open until the worker
  finishes it. ADR-041, ADR-042.
- **Cage**: a rootless podman container a session runs in, reaching other
  agents only through its own. `internal/agent/cage.go`; ADR-037, ADR-044.
- **Sealed cage**: a cage for code nobody vouches for: the internet and
  nothing local, it only finishes its own tasks, and its results leave
  through an outbox. `internal/agent/sealed.go`; ADR-045;
  `docs/agents-files.md`.
- **Files between agents**: a session can push a file to another that takes
  files from it. ADR-048.
- **No mesh**: the agent on one machine alone, on its socket
  (`agent.SocketPath`), this machine standing in as `agent.LocalAddr`.
  ADR-056.

## Around it

- **Basecamp**: Logos's desktop app shell. This repository's modules are
  `basecamp/` (Shrooms), `basecamp/core` (`shrooms_core`) and
  `basecamp-agents/` (Shrooms Agents). ADR-025, ADR-050.
- **Logos Delivery**: the messaging network (Waku) the mesh finds peers on,
  through liblogosdelivery. `internal/waku`.
