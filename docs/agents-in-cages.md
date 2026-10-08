# Shrooms Agents in cages, and off the machine

**Status:** research and a proposal, 2026-10-05; the open questions decided 2026-10-06
(see the end; recorded as [ADR-037](adr/037-agents-in-cages.md)). Step 1, cages
on the agent's own machine, built 2026-10-08 — see "As built" below.

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
- **The image: a small workbench of ours, by default.** Debian slim with git,
  curl, a compiler toolchain, Node (Claude Code), pi and Python; anything else
  the agent installs itself. Published like the agent's own image, separate
  from `shrooms:latest`. The image is a setting — per machine in the agent's
  config, and overridable per session in "+ session" — so a project with its
  own toolchain image can use that instead.
- **Credentials.** Claude Code's login: the owner's `~/.claude` mounted (it
  refreshes its token there), or a long-lived token from `claude setup-token`
  in the environment. pi: its settings mounted, a local model reached over
  the network as now.
- **Network.** Podman's default rootless network reaches the internet and,
  through the host's routes, the mesh — unlimited by default. An allow-list
  (the model's API and what else you name) comes later as a choice per
  session, through an egress proxy the container is pointed at.
- **What it does not stop:** a session can still spend tokens, reach the
  internet and the mesh, and do anything inside its project. It cannot read
  the rest of the machine, take it over, or keep anything once deleted.

## As built (2026-10-08)

`internal/agent/cage.go`. "In a cage" in "+ session" — Basecamp and the
phone — where the agent found podman (`--cages`, on by default;
`--cage-image` for another machine-wide image, or `{"image": …}` per
session over the API).

- **The container** is made with the session's first start, named
  `shrooms-<session>-<6 hex>` (a rename leaves it be), from the workbench
  image with `sleep infinity`. The harness runs in it with
  `podman exec -i`; when its process ends — idle, a restart, an error — the
  container is stopped, which ends anything the session left running, and
  started again with the next one. Deleting the session deletes it
  (`podman rm -f`). Limits: 4 GB of memory, 2 CPUs, 2048 processes.
- **The workbench image** (`internal/agent/workbench.Containerfile`, embedded
  in the agent): node 22 on Debian bookworm with git, a C toolchain, Python,
  ripgrep, jq and pi. Built on the machine with the first caged session
  (a few minutes; until then a message is answered "being built"). Claude
  Code is not in it: the machine's own program is mounted read-only, so a
  cage runs the version the machine does and needs no rebuild when it
  updates.
- **What it sees, at the machine's own paths:** the project (read-write);
  the files sent to sessions (`uploads/`, read-only); the harness's settings
  and transcripts — `~/.claude` for Claude Code, `~/.pi/agent` for pi — so a
  caged conversation resumes, is searched and read like any other;
  shrooms-agent and the daemon's socket, for the shrooms MCP tools. Nothing
  else of the owner's home. `HOME` is the owner's; Claude Code's settings
  file goes to `~/.claude/.claude.json` (`CLAUDE_CONFIG_DIR`), since the one
  beside the home directory is not there.
- **Its environment:** model and provider keys and settings from the agent's
  (`ANTHROPIC_*`, `*_API_KEY`, `*_TOKEN`, `*_BASE_URL`, `PI_*`, …, but not
  the CLAUDE_CODE_ variables of a session the agent itself runs under),
  `SHROOMS_AGENT_SESSION`, and `IS_SANDBOX=1` — Claude Code refuses to skip
  permissions as root anywhere else. Written to a file only the owner reads
  (`cages/<container>.env` in the state directory), not the command line.
- **Network:** pasta, with an address of its own (`fd5e:ca9e:1::2`) and a
  default route. Left to itself pasta copies one interface's addresses and
  routes — on a machine with no IPv6 default route, a mesh's — and the other
  meshes, and this machine's own agent, were not reachable. With it every
  mesh is, through the machine's own sockets: the agent sees the cage's
  requests as from this machine, so `task_update` works from inside.
- **Two images of ours**, from the one Containerfile's stages: the
  *workbench*, and the *desktop* — the workbench with Xvfb, xdotool,
  ImageMagick, ffmpeg and what Qt apps and browsers load, AppImages
  unpacking themselves (no FUSE in a cage): to run, drive and record an app,
  as a reviewer does (1.4 GB). Both have the GitHub CLI. Each is labelled
  with a hash of the Containerfile and built again when an agent has a newer
  one — at its start for the images caged sessions use, and when a cage is
  made; the old one serves meanwhile. Built on the machine's network: a
  build through pasta timed out fetching from npm on the laptop.
- **Options per cage:** *nix* — the machine's store and the owner's profile
  (on PATH); builds go through the daemon where there is one, and a
  single-user nix (the laptop's) is written by the cage, as the owner would.
  *GitHub* — `~/.config/gh`, read-only. Neither by default.
- **Moving a session in, between cages, and out**
  (`POST /v1/sessions/{name}/cage`; "cage" by an open session in both apps):
  only while idle; the process is stopped and the next message resumes the
  conversation where it now runs — the same paths inside, so it is the same
  conversation. A cage changed or left is deleted, with what was installed in
  it. A `caged` event notes it in the conversation.
- **Work left running:** the task watchdog's "is a background command still
  running" looks in the cage (`podman top`), not at the podman client's
  children.

Checked for real on the laptop (`SHROOMS_REAL_CAGE=1 go test ./internal/agent
-run TestARealCage`): a Claude Code turn in a cage, as root, installing a
package with apt and writing into the project a file the owner owns.

## Stronger walls: micro-VMs

- **`podman --runtime krun`** (libkrun, KVM): the same command, each container
  a small VM with its own kernel. The cheapest step up — one flag — but
  rootless krun's networking still has open issues (2026), so it would be
  tried before offered.
- gVisor (`runsc`) is the alternative: a kernel in user space, no KVM needed.
- Firecracker and Kata are heavier to fit and buy little over krun here.

## Off the machine: Akash

**What would run there:** one deployment per agent, with the agent, its harness, the
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

**What there is already (2026-10-08):** a session can make another with the
agent's API — `POST /v1/sessions`, from its shell — as
jimmy-crib/vpavlin made jimmy-crib/publisher; the mesh is the
authentication, so any agent can, on any machine. Then they work together
with tasks (ADR-042). What is not there is what makes it safe to leave to
them: the link between the two, limits, and a cage by default. The rest of
this section is that.


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

## Decided (2026-10-06)

- **A container per session**, not one shared by a machine's sessions.
- **One workbench image by default, customisable** — per machine, and per
  session.
- **On Akash, one agent per deployment.**
- **No egress limit by default;** a per-session allow-list is a feature for
  later.
