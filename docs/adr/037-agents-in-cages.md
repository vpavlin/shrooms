# 037. Agents in cages, and off the machine

**Status:** accepted 2026-10-06, not built — research and plan in
[docs/agents-in-cages.md](../agents-in-cages.md)

## Context

A Shrooms Agents session runs as the machine owner's user, with everything that
user can reach (ADR-036). With auto-approve on, that is the whole home
directory, and whatever a session installs stays on the machine after the
session is gone. Separately, the owner wants sessions — and later sub-agents an
agent starts itself — to run elsewhere: in a stronger sandbox, or on a rented
machine on Akash with a GPU.

The agent already starts every session as a process it talks to over
stdin/stdout through a harness (`docs/agents-harnesses.md`), so a sandbox can
be a wrapper around that command, with the protocol, the apps and the mesh
untouched. A shrooms node, on the other hand, needs `NET_ADMIN` and
`/dev/net/tun` ([docs/a-node-in-a-container.md](../a-node-in-a-container.md)),
which an unprivileged Akash deployment does not have — the reason the blind
relay there is a plain UDP server (ADR-034, `deploy/akash/`).

## Decision

- **A rootless podman container per session.** Root inside, so the agent can
  install what its work needs; the owner's user outside, so the project's files
  stay the owner's and the rest of the machine is out of reach. Kept between
  messages, deleted with the session. The agent daemon stays on the host, with
  its mesh addresses.
- **One workbench image by default, customisable** per machine and per session.
- **No egress limit by default.** A per-session allow-list through an egress
  proxy is a later feature.
- **micro-VMs (`podman --runtime krun`) as a stronger cage**, offered only once
  rootless networking with it works on our machines.
- **On Akash, one agent per deployment**, joining the mesh as a machine. That
  needs **shrooms in user space** — the overlay as a network stack inside the
  process (wireguard-go's netstack, as Tailscale's `tsnet`), since no TUN is to
  be had — with the agent as its first user. It joins with a short-lived
  credential, so a forgotten deployment drops off the mesh by itself.

## Consequences

- A cage contains the machine, not the session's reach: it can still spend
  tokens, reach the internet and the mesh, and do anything inside its project.
- Claude Code's login has to be handed to the container (its directory mounted,
  or a long-lived token).
- An Akash provider can read the deployment's disk and memory. A login, an API
  key or private code sent there is shared with the provider, which limits
  Akash to open work and, above all, open models on rented GPUs.
- Shrooms in user space is a second way to run the core, to be kept working
  beside the TUN one.
- Starting agents remotely costs money: a remote spawn always asks the owner,
  and spawning counts against a depth and a budget.

## What would change our mind

Akash granting `NET_ADMIN` and a TUN device (no user-space shrooms needed for
it); providers with attested hardware (private work could go there); rootless
podman proving unworkable for Claude Code's login or tools, which would push
towards one container per machine, or micro-VMs only.
