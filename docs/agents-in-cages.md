# Shrooms Agents in cages, and off the machine

**Status:** research and a proposal, 2026-10-05. Nothing built. For a decision.

Two wishes that share a mechanism:

1. **Guardrails.** A session that cannot touch more than its project: its own
   filesystem, limits on memory and CPU, nothing of the host's but what it is
   given — so an agent with auto-approve on is a contained risk, and a session
   can be thrown away with everything it installed.
2. **Spinning out.** Sessions — and, with tasks (`docs/agents-together.md`),
   sub-agents an agent starts itself — running somewhere other than the
   machine you are on: a container here, a micro-VM, or a rented machine on
   Akash with a GPU.

## Why it fits what exists

The agent already starts every session as a process it talks to over
stdin/stdout, through a harness (`docs/agents-harnesses.md`): `claude -p`,
`pi --mode rpc`. A cage is a wrapper around that command —

```
podman run --rm -i --name agent-<session> -v <dir>:/work:Z -w /work \
  --memory 4g --cpus 2 --pids-limit 512 <image> claude -p …
```

— and the codec, the events, the apps and the mesh are untouched. The agent
daemon stays on the host, with its mesh addresses and the shrooms socket; only
the session's process is inside. A session gets a "cage" setting, chosen in
"+ session" next to the harness, and shown on it.

## A cage on the agent's own machine: rootless podman

- **Root inside, the owner outside.** Rootless podman maps the container's
  root to the user running the agent, through a user namespace. Inside, the
  agent is root: `apt install`, `npm -g`, whatever its work needs. Outside,
  every file it writes in the project belongs to that user, and it has no
  root on the host. (`--userns=keep-id` is the other choice — the same uid
  inside — but then nothing can be installed. Root inside is the right
  default for an agent.)
- **One container per session, kept.** Named after the session, stopped when
  its process ends and started again on the next message, deleted with the
  session: what the agent installed is still there tomorrow, and gone when
  the session is.
- **The image: a small workbench of ours.** Debian slim with git, curl, a
  compiler toolchain, Node (Claude Code), pi and Python; anything else the
  agent installs itself. Published like the agent's own image, separate from
  `shrooms:latest`.
- **Credentials.** Claude Code's login: the owner's `~/.claude` mounted (it
  refreshes its token there), or a long-lived token from `claude setup-token`
  in the environment. pi: its settings mounted, a local model reached over
  the network as now.
- **Network.** Podman's default rootless network reaches the internet and,
  through the host's routes, the mesh. Cutting it to an allow-list (the model's
  API only) needs an egress proxy — later, and a choice per session.
- **What it does not stop:** a session can still spend tokens, reach the
  internet and the mesh, and do anything inside its project. It cannot read
  the rest of the machine, take it over, or keep anything once deleted.

## Stronger walls: micro-VMs

- **`podman --runtime krun`** (libkrun, KVM): the same command, each container
  a small VM with its own kernel. The cheapest step up — one flag — but
  rootless krun's networking still has open issues (2026), so it would be
  tried before offered.
- gVisor (`runsc`) is the alternative: a kernel in user space, no KVM needed.
- Firecracker and Kata are heavier to fit and buy little over krun here.

## Off the machine: Akash

**What would run there:** one deployment with the agent, its harness, the
session's tools — and, for the interesting case, a model on a rented GPU
(pi with vLLM or Ollama beside it). It joins the mesh as a machine; the apps
list it like atlas; usage (`GET /v1/usage`) counts it like any other, so what
it costs and who used it shows on the dashboard.

**The obstacle: shrooms needs a TUN device.** A node needs `NET_ADMIN` and
`/dev/net/tun` (`docs/a-node-in-a-container.md`). Akash deployments run
unprivileged — which is why the blind relay there is a plain UDP server
(`deploy/akash/`). So an agent on Akash needs **shrooms in user space**: the
overlay as a network stack inside the process — wireguard-go's `tun/netstack`,
the way Tailscale's `tsnet` works — with the agent's HTTP server listening on
its mesh address *inside* its own process. ADR-002 already chose userspace
WireGuard; this is the same engine without the kernel's TUN. As a Go
package any program can embed, the agent is its first user. The delivery node
runs in the container as it does on a phone (outbound only, Edge), and the
relays already on Akash carry what NAT will not.

**Joining:** the deployment carries a single-use invite or a member credential
with a short expiry (members' credentials already expire), so a forgotten
deployment drops off the mesh by itself; revocation as for any device.

**Trust — the real limit:** an Akash provider can read the container's disk
and memory. A Claude login, an API key or private code sent there is shared
with that provider. Fits: open-source work, and above all **open models on
rented GPUs**, where there is no account to leak. Anything else waits for
providers with attested hardware, if and when Akash offers them.

**Control and cost:** deployments created and closed through the Akash
Console's API or `provider-services`, paid in AKT (Console Air); a budget per
deployment, and nothing remote started without your yes.

## Agents starting agents

With tasks (`docs/agents-together.md`), one tool: `spawn(where, harness,
task)` — `where` being this machine in a cage, a micro-VM, or Akash. The new
session records its parent; its result comes back as the task's reply; the
activity view shows the tree. The guardrails there apply here, and more:
spawning counts against a depth and a budget (tokens locally, AKT remotely),
and a remote spawn always asks you first.

## Order of work

1. **Cages on the agent's machine** — rootless podman, a container per
   session, the workbench image, "+ session" offering it. (~3–4 days)
2. **krun** as a stronger cage, once rootless networking is checked on
   atlas and the laptop. (~1 day to try)
3. **Shrooms in user space** — the netstack package. The prerequisite for
   Akash, and useful wherever a TUN cannot be had. (~1–2 weeks)
4. **An agent on Akash** — image, SDL, the join, a GPU model. (~3–5 days,
   after 3)
5. **Spawning**, locally then on Akash — after tasks. (~3–4 days)

## Open questions

- A cage per session, or one per machine shared by its sessions?
- The workbench: one image for all, or per language (and who keeps them current)?
- On Akash: one agent per deployment, or an agent hosting many sessions?
- Is an egress allow-list wanted from the start, or only on request?
