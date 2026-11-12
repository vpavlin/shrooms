# Sessions in cages

Auto-approve is what makes an agent useful from a phone: you send a message,
put the phone away, and the work gets done without you approving every
command. It also means the agent runs any command it decides to, as your user,
with everything your user can reach. A cage lets you keep auto-approve without
lending the session the whole machine.

A caged session runs in a rootless podman container of its own. Inside, the
agent is root, so it can `apt install` whatever its work needs. Outside, it is
your user and nothing more, and of your home directory it sees only what it
needs: its project, and the settings of the harness that runs it. What it
installs stays in the container until the session is deleted.

## Not caged, caged, sealed

Three choices, one sentence each, and kept true:

- **Not caged:** your agent, with your machine. It can do what you can.
- **Caged:** your agent, kept in its folder. It sees its project and its own
  conversations, and nothing else of yours: no credentials, no other
  conversations, nothing else of your home. The network stays open, and its
  memory and CPUs are limited. This is against a careless agent, not a
  hostile one.
- **Sealed:** for code or content you do not trust. The internet and nothing
  local, results only through its outbox (below).

What makes "caged" true:
- **A private harness home.** A caged session's `~/.claude` (or
  `~/.pi/agent`, or OpenCode's config and data) is a directory of the cage's
  own. Your settings, skills and plugins are let into it read-only. Its own
  conversations are let in read-write, from the real folder for its project:
  `~/.claude/projects/<its folder>`, or pi's `sessions/<its folder>`.
  OpenCode keeps every project's conversations in one database, so a caged
  OpenCode session has a database of its own, and its conversations go with
  its cage; its config file is not let in either, since a provider's key
  lives in it. Nothing else is let in: not other sessions' conversations, not
  `history.jsonl` (every prompt typed), not running sessions' keys, backups
  of your config, or anything someone keeps there later. It is built from
  what is let in, so what lands there later is out by default.
- **No login and no keys.** The cage reaches its model through the agent,
  which adds the credential on the way out (ADR-059).

Its conversation is still the real file in your `~/.claude`, so `claude
--resume` in a terminal, the apps' history and taking the session out of the
cage all work as before. Sessions in the same working folder share it: they
see each other's conversations, as they would outside a cage.

Why it is built this way is in
[ADR-037](../adr/037-agents-in-cages.md); the research, and what comes after
(micro-VMs, Akash, agents starting agents), is in
[the cages research and plan](../agents-in-cages.md).

## What a cage protects, and what it does not

A cage contains the machine. It does not contain the session's reach. Be
clear about the difference before you rely on it.

A caged session **cannot**:

- read or change the rest of your home directory: your SSH keys, browser
  profiles, password stores, other projects, your shell's rc files;
- become root on the machine, or touch system files;
- leave anything behind once the session is deleted: the container goes with
  it, and everything that was installed in it;
- keep processes running after its turn's process ends: the container is
  stopped, and with it every server or watcher the session started.

A caged session **can still**:

- **spend tokens.** It holds no login and no key, but its model requests go
  through the agent, which adds your credential on the way out (ADR-059): it
  spends on your account as freely as a session outside a cage.
- **reach the internet.** There is no egress limit. An allow-list is planned,
  not built.
- **reach the mesh** — every service on it — but **not any agent directly**
  ([ADR-044](../adr/044-cages-reach-agents-through-their-own.md)). The agent
  port is closed inside every cage, by a rule its root cannot remove. The
  shrooms tools (`list_agents`, `ask_agent`, `task_status`, `task_update`,
  `task_ack`, `send_file`, `publish_file`, `schedule`) work through a socket
  to the cage's own agent, mounted at `/run/shrooms-agent/proxy.sock`, which
  forwards only those: `publish_file` as its own session on its own machine,
  `schedule` its own calendar only. It cannot make, delete or reconfigure a
  session, so a caged session cannot start an uncaged one, here or anywhere.
  Its agent names it to the receiver itself, and says it is caged. Each
  session decides which caged agents it takes tasks from: every one, or
  those you allow by name ("caged tasks" in the apps; by default caged
  sessions take them from every cage and the others from none). A task from
  any other cage is held for you to allow or decline, not run
  ([ADR-070](../adr/070-the-owner-allows-each-cage.md)). Files it takes only
  from the sessions it allows. A sealed cage gets only `task_update` (below).
- **do anything inside its project.** The project is mounted read-write. That
  includes `.git/hooks`, a `Makefile`, a `package.json` script: anything you
  later run from that directory outside the cage runs as you.
- **write its own conversations.** Its project's conversation folder is your
  real one, read-write, so it can write anything into its own transcripts.
  What the harness *runs or obeys* comes into its private home read-only:
  Claude Code's `settings.json`,
  `settings.local.json`, `CLAUDE.md`, `hooks/`, `skills/`, `plugins/`,
  `commands/`, `agents/`, `output-styles/` and `bin/`; pi's `settings.json`,
  `models.json`, `mcp.json`, `extensions/`, `skills/`, `prompts/`,
  `themes/`, `AGENTS.md`, `SYSTEM.md`, `APPEND_SYSTEM.md` and `bin/`;
  OpenCode's `AGENTS.md`, `package.json` and `node_modules/`. A caged session
  cannot plant a hook that a session outside the cage would run.

Three options widen it further, and none is on by default:

- **GitHub login** mounts `~/.config/gh` read-only. The session can then push,
  open pull requests and do anything else your `gh` login can, on every
  repository you can reach.
- **nix** gives it the machine's nix: the store, your profile and your nix
  settings. On a multi-user nix the store is read-only and builds go through
  the nix daemon, as for any user. On a **single-user nix** there is no
  daemon, so the cage writes `/nix` itself, as you would. A session can then
  change what is in the store and in your profile, which the rest of the
  machine runs.
- **shared folder** mounts a folder at `/shared`, read-write, that every
  other caged session given it sees too, on the same machine and the same
  meshes (ADR-057). A session can then leave a clone or a dataset there for
  the others, and anything it writes there the others may read or run.

So a cage is the right tool against an agent that does something careless
with your machine: `rm -rf` in the wrong place, a global install that breaks
your toolchain, a stray server left running. It is not a security boundary
against a session that is deliberately turned against you, for example by a
prompt injection in a web page it read. For that, keep secrets out of the
agent's environment, leave the two options off, and review what it pushes.


## Sealed cages: for code nobody vouches for

For reviewing someone else's code — a contributor's pull request, an
application a stranger built — tick **sealed** beside "in a cage"
([ADR-045](../adr/045-sealed-cages-for-code-nobody-vouches-for.md)). A
sealed cage is an ordinary cage, and also:

- **the internet and nothing local**: no LAN (your router, your NAS, the
  machines beside it), no mesh over IPv6 or IPv4, no link-local, on any
  port. DNS goes to public resolvers (1.1.1.1, 9.9.9.9), so mesh names do
  not resolve, and a network that blocks public DNS leaves it without. The
  cage's own loopback (an app it starts to test) and the internet work. Set
  from outside, like the agent-port rule; root inside cannot remove it.
- **a project, not a home**: its directory is mounted read-write, so it
  cannot be your home, anything above it, or a directory holding `.ssh`,
  `.gnupg`, `.aws`, `.config/gh` or `.claude`.
- **its own login**: Claude Code runs on the machine's *sealed token*, not
  on your `~/.claude`. The cage has none of your settings, none of your
  other conversations, none of the keys in the agent's environment. Claude
  Code only; GitHub, nix and the shared folder are not offered.
- **asks nothing**: it can finish the tasks it is given, and cannot ask
  another agent for anything or send it a file.
- **no user namespaces**: a seccomp profile (podman's default, with
  `CLONE_NEWUSER` refused) closes the door most container escapes go
  through. Programs and threads run as usual; `bwrap`, rootless podman and
  the like do not, inside a sealed cage.
- **its conversation only**: a session moved into a sealed cage keeps its
  conversation (that transcript is copied in, no other), so the code under
  review could read it. For a review, a new session is the clean start.
- **an outbox**: `~/shrooms-outbox/<session>` on the machine, `/outbox`
  inside, for its results. Read what is in it as untrusted text: a review of
  hostile code can carry instructions for whoever reads it. It can hold
  links that point anywhere on the machine; packing never follows them, and
  neither should you. The machine's agent carries it out: to the asker when
  a task finishes, if the asker takes files from the session, or by hand
  with `shrooms-agent outbox send [--only FOLDER] SESSION MACHINE/SESSION`
  ([Files between agents](../agents-files.md)).

**After a sealed session: the directory is untrusted.** The code under review
could write in its project, and leave something there that runs later,
outside the cage, as you: a `.git/hooks` script, a Makefile target, an
`.envrc`, an npm `postinstall`, an editor's workspace settings. No mount
prevents that without breaking the review. So run nothing from that directory
outside a cage — no git command that runs hooks, no build, no `direnv allow`.
Read it, then delete it, or clone again to go on.

**Giving a machine its sealed token, once:**

```
claude setup-token
install -m 600 /dev/stdin ~/.local/share/shrooms-agent/sealed-claude-token
```

Paste the token `claude setup-token` printed into the second command, then
Ctrl-D. It belongs to your subscription and can be revoked from your
account; `--sealed-token FILE` keeps it elsewhere. Until it exists, the apps
show how instead of the option.

## Requirements

- **Podman, rootless**, for the user the agent runs as. Check with
  `podman run --rm docker.io/library/debian:bookworm-slim true` as that user.
  Rootless podman needs subordinate uid and gid ranges for the user in
  `/etc/subuid` and `/etc/subgid`; most distributions add them when the user
  is created.
- **pasta** (the `passt` package). Cages use it for their network, with
  options the older slirp4netns does not have. Podman 5 uses pasta by
  default; on podman 4 install `passt` yourself.
- **cgroups v2**, with the memory, cpu and pids controllers delegated to the
  user, for the limits each cage gets: 4 GB of memory, 2 CPUs and 2048
  processes, unless a session is given memory and CPUs of its own (below,
  "A cage's memory and CPUs"). The machine's are fixed; there is no flag to
  change them. If podman cannot apply them, it refuses to make the container
  and the session's first message fails with podman's error.

The agent looks for `podman` on its `PATH` when it starts. If it finds it, its
log says `cages offered`, and "in a cage" appears in "+ session" in both apps.
If it does not, or the agent runs with `--cages=false`:

- the apps do not offer cages for that machine;
- creating a caged session over the API fails with "this machine has no
  podman to cage sessions with";
- a session that was caged before keeps its cage in its settings, and its
  next message fails with "this session is caged, and this machine has no
  podman to cage it with". You can still take it out of the cage.

A machine with docker and no podman gets no cages.

## Choosing an image

There are two images of ours, built from one Containerfile that ships inside
the agent (`internal/agent/workbench.Containerfile`):

| Image | Name | What is in it |
|---|---|---|
| workbench | `localhost/shrooms-workbench:latest` | Node 22 on Debian bookworm, git, curl, a C toolchain, Python with pip and venv, ripgrep, jq, pi, the GitHub CLI |
| desktop | `localhost/shrooms-workbench:desktop` | the workbench, plus Xvfb, xdotool, ImageMagick, ffmpeg, fonts and the libraries Qt apps and browsers load (about 1.4 GB) |

Claude Code is in neither. The machine's own `claude` program is mounted into
the cage read-only, so a cage runs the same version as the machine and needs
no rebuild when Claude Code updates. OpenCode is mounted the same way. pi is
different: a caged pi session runs the pi installed in the image.

The workbench is the machine's image unless you start the agent with
`--cage-image` (see [Setting up](setup.md)). Whatever the machine's image is,
the apps offer it first and the two of ours after it. Over the API, a session
can name any image with `{"image": "…"}`.

**Our images build themselves.** The first time a cage needs one that is not
there, the agent builds it on the machine, in the background. That takes a
few minutes for the workbench and longer for the desktop. The apps say so
beside "in a cage", and a message sent meanwhile is refused with:

```
the image for this session's cage is being built (a few minutes, the first time); send again then
```

Each image is labelled with a hash of the Containerfile it was built from.
When a newer agent brings a changed Containerfile, it builds the image again,
at its start for the images caged sessions use, and when a cage is made. The
old image serves until the new one is ready. Containers already made keep the
image they were made from; moving the session out and back in gives it the
new one.

**Your own image** is yours to provide: the agent does not build or pull it.
Pull or build it first (`podman pull`, `podman build -t`), or the session's
first message fails with "there is no image … on this machine". It needs
`sleep` on its `PATH` (the container runs `sleep infinity`), and Node if you
run Claude Code from npm rather than its native build. Root inside is
assumed.

## Starting a caged session

In "+ session", in Basecamp or on the phone, tick **in a cage**. The line
beside it says what a cage is given, and whether the image is ready, being
built, or failed to build last time. Under it:

- the image: the machine's, the workbench, or the desktop;
- **nix**, shown only where the machine has `/nix/store`;
- **GitHub login**;
- **shared folder**.

Turn on auto-approve as you would for any session. The same choice is offered
when you take over a conversation started in a terminal.

The container is not made yet. It is made with the session's first start,
when you send the first message. A caged session carries the violet
**CAGED** tag in the list and on the board, and "in a cage (…)" in its header.
In Basecamp, hover over the tag for the image and options.

Over the API, add `cage` to the request: `{}` for the machine's image.

```
curl -X POST "http://[MESH-ADDRESS]:7387/v1/sessions" \
  -d '{"name": "webapp", "dir": "~/src/webapp", "auto_approve": true,
       "cage": {"image": "localhost/shrooms-workbench:desktop", "github": true}}'
```

`GET /v1/harnesses` answers with a `cage` object as well: whether cages are
`available`, the machine's `image`, the `images` offered, `nix`, `ready`,
`building`, the `error` of the last failed build, and `shared`, `shared_in`
and `shared_root` for the shared folder (below).

## Moving a session in and out

"cage" in an open session's header moves it into a cage; on a caged session
it reads "caged" and lets you **CHANGE** the options or **TAKE OUT**. Over the
API:

```
curl -X POST "http://[MESH-ADDRESS]:7387/v1/sessions/webapp/cage" \
  -d '{"cage": {"nix": true}}'
curl -X POST "http://[MESH-ADDRESS]:7387/v1/sessions/webapp/cage" \
  -d '{"cage": null}'
```

- **Only while idle.** A session that is working, or has a permission prompt
  waiting, is refused (409): move it once it is idle.
- **The conversation continues.** The session's process is stopped, and your
  next message starts it in its new place, resuming the same conversation.
  This works because the paths inside a cage are the machine's own, so the
  harness finds its transcript where it left it.
- **The old container is deleted**, and everything that was installed in it.
  Changing the options gives the session a fresh container, too: the image,
  nix, GitHub and the shared folder are fixed when a container is made.
- A note in the conversation records the move and who made it.

## A cage's memory and CPUs

A cage gets the machine's limits, 4 GB of memory and 2 CPUs, which no flag
changes. A session can have its own. In the cage
dialog, under RESOURCES, set its memory (`16g`, `512m`) and CPUs (`8`, up
to the machine's), or leave them empty for the machine's. Over the API:

```
curl -X POST "http://[MESH-ADDRESS]:7387/v1/sessions/review/cage/limits" \
  -d '{"memory": "16g", "cpus": "8"}'
```

They are set on the cage as it is (`podman update`), running or not. It is
not made again, so what was installed in it stays: a nix, a toolchain, a
build. A caged agent cannot change its own limits.

## What the agent inside sees

**Files, at the machine's own paths:**

- the project, read-write;
- the files sent to sessions (`uploads/` in the agent's state directory),
  read-only;
- for Claude Code, a private `~/.claude` of the cage's own, with your
  settings, skills and plugins in it read-only and its own project's
  conversations read-write, and the directory of the machine's `claude`
  program read-only; for pi, the same with `~/.pi/agent` and its own
  `sessions/` folder; for OpenCode, a private config and database, and its
  program read-only (above, "Not caged, caged, sealed");
- `shrooms-agent` itself, for the shrooms MCP tools, and the cage's socket to
  its own agent at `/run/shrooms-agent/proxy.sock`, which they go through.
  **Not** the shrooms daemon's control socket: your user's tier on it can
  leave or join a mesh, change the relay and the services, and restart the
  daemon. It was mounted until 2026-10-09, and cages made with it are made
  again;
- with nix: `/nix`, `~/.local/state/nix` and `~/.config/nix`; with GitHub:
  `~/.config/gh`;
- with the shared folder: a podman volume at `/shared`, with
  `SHROOMS_SHARED=/shared` in its environment.

**The shared folder** (ADR-057) is one per set of meshes: `shrooms-shared`
for sessions on every mesh, `shrooms-shared-LABELS-HASH` for a session
limited to some. So a session limited to the home mesh shares nothing with an
office one. Changing a session's meshes stops it, and its next start makes
its cage again with the other folder. On the machine it is podman's volume
directory (`shared_root` in `GET /v1/harnesses`, usually
`~/.local/share/containers/storage/volumes`), then `VOLUME/_data`; the list
gives each session's volume as `cage.shared`, and the cage dialog shows the
path with OPEN. Treat what is in it as written by an agent: a link there can
point anywhere on your machine when you follow it outside a cage. The agent
makes the volume itself and refuses one of that name defined any other way
(a bind to a directory of yours, say), and `podman volume prune` deletes it
once no container uses it.

Nothing else of your home directory is there. Files the agent writes into the
project belong to your user outside, even though it wrote them as root.

**`HOME` is your home directory's path**, so `~/.claude` resolves as on the
machine. Claude Code's settings file, normally `~/.claude.json` beside the
home directory, goes to `~/.claude/.claude.json` instead
(`CLAUDE_CONFIG_DIR`), since nothing beside it is mounted.

**Environment.** The cage gets its own `PATH` (the image's, with your nix
profile in front when it has nix), `SHROOMS_AGENT_SESSION`, `IS_SANDBOX=1`,
the gateway's base URL and a dummy key (ADR-059), and of the agent's own
environment only what is named:

- `TZ`, `LANG`, `LANGUAGE` and `LC_*`;
- the proxy variables (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `ALL_PROXY`,
  in either case);
- the settings its harness asks for: for Claude Code, its model choices
  (`ANTHROPIC_MODEL` and the like), output and thinking limits, and its
  telemetry switches.

Nothing else passes, whatever its name: no key or token (its model is
reached through the agent, which holds them), not your `PATH`, `DISPLAY`,
`SSH_AUTH_SOCK` or `USER`, and nothing added to the agent's environment
later. A GitHub login reaches a cage only as the `gh` login mounted
read-only. The variables are written to a file only you can read
(`cages/<container>.env` in the agent's state directory,
`~/.local/share/shrooms-agent` by default) rather than on podman's command
line.

**Network.** The cage has an address of its own (`fd5e:ca9e:1::2`) and a
default route through pasta, which carries its traffic out on the machine's
own sockets. To the internet and to the mesh, it looks like this machine.

## A GUI in the desktop cage

The desktop image has what it takes to run an app without a screen, drive it
and record it: what a reviewer needs. Nothing is started for you; the agent
starts the X server itself, for example:

```
Xvfb :99 -screen 0 1920x1080x24 &
export DISPLAY=:99
xdotool search --name "My App" windowactivate
import -window root shot.png
ffmpeg -f x11grab -video_size 1920x1080 -i :99 -t 30 run.mp4
```

AppImages run: there is no FUSE in a cage, so the image sets
`APPIMAGE_EXTRACT_AND_RUN=1` and they unpack themselves on each start. Xvfb
and the app end when the session's process does, with everything else the
session left running; the agent starts them again on its next turn.

## Checking on a cage from a shell

On the machine, as the agent's user. Each container is named
`shrooms-<session>-<6 hex characters>`, chosen when the session is made or
moved into a cage, so a renamed session keeps its container and a new session
of an old name gets its own. Each carries a label with the session's name:

```
podman ps -a --filter label=xyz.vpavlin.shrooms.session
podman ps -a --filter label=xyz.vpavlin.shrooms.session=webapp
```

A container that is "Exited" is normal: it is stopped whenever the session's
process ends, and started with the next message.

To look inside, or at what is running:

```
podman exec -it shrooms-webapp-3f9a1c bash
podman top shrooms-webapp-3f9a1c args
```

The agent runs the same `podman top` to see whether a background command is
still running after a turn ended: then a session with an open task is not
reminded of it yet.

To try a cage for real from a checkout of the repository (it runs a Claude
Code turn that installs a package with apt and writes a file into a project,
then checks the file is owned by you):

```
SHROOMS_REAL_CAGE=1 go test ./internal/agent -run TestARealCage -v
```

The workbench image has to be built already: start a caged session once, or
the test's message is refused as "being built".

## Cleaning up

Deleting a session deletes its container (`podman rm -f`), everything
installed in it, its environment file, its private harness home and the
gateway's files, in `cages/` in the agent's state directory. Claude Code's
and pi's conversations, in your own folders, stay; a caged OpenCode
session's, in its own database, go with it. Taking a session out of its cage
does the same. You should not need to do more.

If containers are left over, for example from a session deleted while podman
was not answering, list them with the label filter above and remove those
whose session no longer exists:

```
podman rm -f shrooms-oldsession-a1b2c3
```

The images stay until you remove them. A rebuild leaves the previous image
untagged:

```
podman image prune
podman rmi localhost/shrooms-workbench:desktop
```

A removed image of ours is built again the next time a cage needs it.

## Troubleshooting

**The image does not build.** The apps show the last line of the build's
error beside "in a cage", and `GET /v1/harnesses` has it in `cage.error`; the
agent's log has the whole run. Builds use the machine's network
(`--network host`), so a failure to fetch from Debian, npm or GitHub is the
machine's own network or a proxy. Check disk space as well: the build cache
and the desktop image take a few GB. Sending a message to a caged session
tries the build again.

**The first message is refused as "being built".** The image is being built;
send it again in a few minutes. Nothing was lost; the message was not sent.

**A cage cannot reach the mesh.** Check from inside, against this machine's
agent:

```
podman exec shrooms-webapp-3f9a1c curl -s "http://[MESH-ADDRESS]:7387/v1/harnesses"
```

A container keeps the network it was made with. One made by an agent older
than the pasta address above copies one of the machine's interfaces and
reaches only one mesh, if any. Move the session out of its cage and back in
to get a new container. If a new container cannot reach it either, check that
the machine itself reaches the address, and that pasta is installed.

**Claude Code refuses to skip permissions as root.** It does that everywhere
except a sandbox, which it detects by `IS_SANDBOX=1`. The agent sets it for
every caged session. If you run `claude` yourself in a cage with
`podman exec`, pass it: `podman exec -it -e IS_SANDBOX=1 …`, or
`--env-file` with the session's environment file.

**The shrooms tools fail inside a cage.** The `shrooms-agent` program is
mounted when the container is made. If the agent's binary has since moved to
another path, the container lacks it: move the session out and back in. The
tools reach agents only through `/run/shrooms-agent/proxy.sock`, which the
agent serves while it runs.

**A message fails with a podman error about cgroups or controllers.** The
limits could not be applied: see Requirements.

## See also

- [Using it](using.md): the apps, including the CAGED tag and the cage dialog
- [Setting up](setup.md): installing podman, `--cages` and `--cage-image`
- [Agents together](together.md): the shrooms tools a caged session also has
- [the cages research and plan](../agents-in-cages.md) and
  [ADR-037](../adr/037-agents-in-cages.md): the design
- [the reference](../agents.md): Shrooms Agents as a whole
