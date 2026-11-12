# Setting up Shrooms Agents

This page takes a machine to one that serves its coding agents to Basecamp
and, once it is in a shrooms mesh, to your phone and your other machines. It covers the agent on each
machine, Claude Code, pi and OpenCode as the agent runs them, voice notes, cages, and
getting the two apps. The last part is what to check when something does not
connect.

Shrooms Agents has three parts:

- **`shrooms-agent`**, a daemon on each machine that runs coding agents. It
  keeps that machine's sessions (a name, a directory, and Claude Code, pi or
  OpenCode working in it) and serves them over HTTP on a unix socket only its
  user can open, and, once the machine is in a mesh, on its mesh addresses,
  port 7387. Nowhere else.
- **The Android app**, Shrooms Agents, a separate app next to the shrooms app.
- **The Basecamp module**, `shrooms_agents`, which uses `shrooms_core` for its
  networking.

The agent has no logins and no tokens. On the machine, the socket's
permissions are the access control; between machines, the mesh is: only
members of your meshes can reach the address it listens on, and every request
is attributed to the device it came from. The reasoning is in
[the reference](../agents.md), "Access: the bind is the access control".

## Before you start

On each machine that will run agents:

- **shrooms, for more than this machine.** Without it the agent serves this
  machine alone, on its own socket
  (`$XDG_RUNTIME_DIR/shrooms-agent/agent.sock`, its user's only), and
  Basecamp's Shrooms Agents finds it there (ADR-056). For your phone and your
  other machines, the machine has to be in a mesh: `shrooms status` lists at
  least one. Install shrooms later and run the installer again; it lets the
  agent read the daemon's socket, and the agent starts serving on the mesh
  within half a minute, without a restart.
- **Linux with systemd.** The agent runs as a systemd *user* service.
- **docker or podman**, whichever shrooms already uses. The installer gets
  the binary out of an image. Without either, give it a binary with
  `--binary` (see below).
- **A user account that is not root.** A session can do anything its user
  can, so the agent refuses to be installed for root. On a desktop, use your
  own account. On a server, a dedicated account is better (see "On a server").
- **The coding agents themselves, set up as that user.** The installer
  installs none of Claude Code, pi or OpenCode. Each must work from a terminal
  as that user first: installed, logged in or given its keys, and paid for.
  The agent only runs what is already there. Install them with their own
  instructions: [Claude Code](https://code.claude.com/docs/en/setup),
  [pi](https://pi.dev), [OpenCode](https://opencode.ai). Ideally before the
  agent; pi or OpenCode installed later is picked up within 30 seconds, as
  long as it lands on the `PATH` the agent started with (see "pi is not
  offered" under "Troubleshooting").

You need at least one of them. A machine with none still runs the agent, but
its sessions have nothing to run.

## Install the agent

1. Log in as the user whose agents are to be served, and download the
   installer:

   ```
   curl -fsSLO https://raw.githubusercontent.com/vpavlin/shrooms/master/scripts/install-agent.sh
   ```

2. Run it with sudo, from that user. It needs root to install the binary,
   the unit, the socket ACL and the firewall rule, and it works out from
   `SUDO_USER` whose agent it is:

   ```
   sudo bash install-agent.sh
   ```

   Add `--voice` to build voice notes as well (see "Voice notes").

3. Read what it printed at the end. A working install ends like this:

   ```
   ==> checking
     serving on its socket: Claude Code,pi
     and on the mesh, port 7387
     voice notes off (--voice to build them here)
   done: the Shrooms Agents app and Basecamp module find it on the mesh.
   ```

   Without a mesh, the second line is `this machine only: Basecamp's Shrooms
   Agents finds it here`, and the last says how to add your other machines
   and your phone.

   It also tells you if Claude Code is missing or not logged in for that
   user. Fix those before you go on (see "Claude Code").

Running the installer again is safe. To update the agent or add `--voice`
later, run it again.

### Installer options

| Option | What it does |
|---|---|
| `--user NAME` | Serve this user's agents instead of the user running sudo. Use it when you are root, or when the agent runs under a dedicated account. |
| `--voice` | Also build whisper.cpp's `parakeet-cli` and download the Parakeet model, checked by sha256, for voice notes. |
| `--files-url URL` | The mesh's file server, where sessions publish files (`publish_file`, ADR-079): any server that takes an HTTP PUT. Written to the drop-in `~/.config/systemd/user/shrooms-agent.service.d/20-files-url.conf` as `SHROOMS_AGENT_FILES_URL`. Without it, the agent keeps published files itself and serves them on its mesh addresses. |
| `--adopt-pi NAME` | Take over a pi agent that already runs on its own (a `pi-agent.service` that keeps pi in tmux) and continue its newest conversation as session `NAME`, kept running. See "pi". |
| `--image REF` | Take the binary from this image. The default is `ghcr.io/vpavlin/shrooms-agent:latest`, which has one static binary per architecture (amd64, arm64). |
| `--binary PATH` | Install this binary instead of pulling an image. No container runtime is needed. In the portable package, `bin/shrooms-agent` next to the script is used automatically. |
| `--uninstall` | Remove the binary, the service, socket access and the firewall rule. Sessions and their history stay. |

To build the binary yourself, with Go installed, run
`go build -o shrooms-agent ./cmd/shrooms-agent` in a checkout and pass it with
`--binary`. It is pure Go, so no C library is needed.

### What the installer changes

You should know about each of these, because each one is a place where
something can later go wrong:

- **`/usr/local/bin/shrooms-agent`**, the binary.
- **Access to the daemon's socket.** If the user cannot already read and
  write `/run/shrooms/shrooms.sock`, the installer grants access with an ACL.
  shrooms runs in a container whose group names are not the host's, so its
  `socket_group` setting cannot name a host group. The ACL survives:
  - reboots, through `/etc/tmpfiles.d/shrooms-agent-USER.conf`;
  - restarts of shrooms (an image auto-update is one), through a drop-in,
    `/etc/systemd/system/shrooms.service.d/20-agent-access.conf`. It keeps
    `/run/shrooms` across a restart and applies the ACL again after every
    start.

  The installer installs the `acl` package if `setfacl` is missing.
- **The firewall.** If firewalld or ufw is active, the installer opens TCP
  7387 to this machine's own mesh addresses only. If neither is active, or
  there is no mesh yet, it does nothing.
- **The service.** `/etc/systemd/user/shrooms-agent.service`, enabled and
  started for the user, with lingering turned on (`loginctl enable-linger`)
  so it keeps running while the user is logged out. It starts the agent
  through a login shell (`bash -lc`), so sessions find the same programs a
  terminal would: `claude`, `pi`, `opencode`, `nix`, `go`. "pi is not offered" under
  "Troubleshooting" covers the catch.

If `~/.config/systemd/user/shrooms-agent.service` exists, it overrides the
installed unit. The installer warns about it. Remove it unless you meant to
keep it.

## Check that it runs

Run these as the agent's user:

```
systemctl --user status shrooms-agent
journalctl --user -u shrooms-agent -n 50
```

At startup the log shows a `serving agents` line for the socket and one for
each mesh, a `harness` line for pi and for OpenCode if they were found, and
either `voice notes on` or `voice notes off` with the reason. Without a mesh
it says `no mesh yet: serving this machine only, on its socket`, with why.

On the machine itself, ask through the socket; a request to the machine's own
mesh address is refused, since any local user could make one:

```
curl --unix-socket "$XDG_RUNTIME_DIR/shrooms-agent/agent.sock" http://agent/v1/harnesses
```

From any other device in the mesh, use the machine's mesh address (shown by
`shrooms status` on that machine):

```
curl "http://[fd00:0:0:0::1234]:7387/v1/harnesses"
curl "http://[fd00:0:0:0::1234]:7387/v1/sessions"
```

The first command lists the coding agents this machine can run. The second
lists its sessions, which is empty on a new install.

## Claude Code

The agent runs Claude Code as `claude -p` with stream-json input and output,
one process per session. A session uses the user's own settings, login,
plugins and `~/.claude`. Claude Code is found on the service's `PATH`. Use
`--claude PATH` if it is somewhere else.

**Logging in must be done on the machine, outside the apps.** `claude -p`
has no slash commands, so `/login` typed into a session answers
"/login isn't available in this environment". On a desktop where you already
use Claude Code, there is nothing to do. On a headless machine, do one of the
following:

1. **Log in once, interactively.** Over ssh, as the agent's user, run
   `claude`, then `/login`. It prints a URL to open in a browser on any
   device and asks for the code back. The login is kept in `~/.claude`, and
   Claude Code refreshes it itself.
2. **Use a long-lived token.** Run `claude setup-token` (on that machine, or
   on any machine logged in to the same account). It prints an OAuth token
   that lasts about a year. Put it in the service's environment:

   ```
   systemctl --user edit shrooms-agent
   ```

   ```
   [Service]
   Environment=CLAUDE_CODE_OAUTH_TOKEN=your-token-here
   ```

   Then restart the agent with `systemctl --user restart shrooms-agent`. The
   token does not refresh, so you have to replace it when it expires.

Claude Code refuses `--dangerously-skip-permissions` as root. That is one
more reason the agent runs as a normal user: auto-approve depends on it. See
[harnesses and logging in](../agents-harnesses.md) for the background.

## pi

pi ([pi.dev](https://pi.dev)) runs on any model provider, including a local
one, so a machine without a Claude account can still serve sessions. The
agent offers pi when it finds `pi` on its `PATH`, at startup or, if pi is
installed later, within 30 seconds; and runs it as `pi --mode rpc`. pi's model, provider and keys come from pi's own settings,
the same as on the command line.

1. **Install pi as the agent's user**, and check that it works from a
   terminal with the model you want.
2. **Configure providers in `~/.pi/agent/models.json`.** It is pi's file,
   documented by pi. The agent reads it too, for one thing: credits (see
   below). Keep keys out of the file by referring to an environment variable:

   ```
   {
     "providers": {
       "venice": {
         "baseUrl": "https://api.venice.ai/api/v1",
         "apiKey": "$VENICE_API_KEY",
         "models": [ ... ]
       }
     }
   }
   ```

3. **Give the service the variables.** The agent runs under systemd, not in
   your terminal, so a key exported in `~/.bashrc` usually does not reach it
   (see "Troubleshooting"). Put the keys in a file only you can read, for
   example `~/.pi/agent/.env`, with lines like `VENICE_API_KEY=...`, and add
   it to the service:

   ```
   systemctl --user edit shrooms-agent
   ```

   ```
   [Service]
   EnvironmentFile=-%h/.pi/agent/.env
   ```

   Then restart the agent with `systemctl --user restart shrooms-agent`.
4. **Optionally, pin a model for every pi session** with `--pi-args`, for
   example `--pi-args "--provider ollama --model qwen3"` (see "Changing the
   agent's flags"). Without it, pi uses `~/.pi/agent/settings.json`.

Things to know about pi:

- **The mesh's agents as tools.** pi reads MCP servers only from
  `~/.pi/agent/mcp.json`, and only from pi 1.0 on: an older pi starts
  without the shrooms tools and does not say so. When the agent starts, it adds an entry called
  `shrooms` there that runs `shrooms-agent mcp`. It leaves the rest of the
  file alone, and it also leaves alone any `shrooms` entry of your own that
  does not look like its own. `PI_CODING_AGENT_DIR`, if set, is used in place
  of `~/.pi/agent`.
- **Credits.** For each provider in `models.json` whose `baseUrl` is
  `api.venice.ai`, the agent asks Venice every few minutes what is left on the
  key (DIEM, USD, and when the daily allowance refills), and the apps show
  it. The key is either written out or `"$VAR"` / `"${VAR}"`, read from the
  agent's own environment. That is the other reason step 3 matters. The apps
  show only a fingerprint of the key, never the key.
- **No approvals.** pi runs its tools without asking, so its sessions have no
  auto-approve switch.
- **Taking over a pi agent that already runs.** If pi already runs on its own
  as a `pi-agent.service` keeping it in tmux (with a heartbeat or a chat
  bridge, say), `--adopt-pi NAME` hands it to the agent. The installer stops
  and disables that service and its tmux session (two pi processes writing
  one conversation would each write a separate branch of it), adds
  `EnvironmentFile=-%h/.pi/agent/.env` to the drop-in
  `~/.config/systemd/user/shrooms-agent.service.d/10-pi-agent.conf`, and
  continues pi's newest conversation as session `NAME`, kept running. If you
  run it again, it finds the session and leaves it as it is.

## OpenCode

OpenCode ([opencode.ai](https://opencode.ai)) is another open harness, on
OpenCode's own models (some of them free) or any provider it is set up with.
The agent offers it when it finds `opencode` on its `PATH`, at startup or, if
it is installed later, within 30 seconds, and runs it as `opencode acp`, its
ACP server (ADR-061). Its providers, keys and settings are OpenCode's own,
the same as on the command line.

1. **Install OpenCode as the agent's user**, and check that it answers from a
   terminal with the model you want.
2. **Give the service its keys**, as for pi: a provider key exported in
   `~/.bashrc` does not reach a systemd service, so put it in an
   `EnvironmentFile=` drop-in (see "pi", step 3). OpenCode's free models need
   no key.
3. **Optionally, a default model for the machine** with `--opencode-model`,
   such as `--opencode-model opencode/…` (see "Changing the agent's flags").
   Without it, OpenCode's own config decides. A session's own model, chosen
   in the apps, wins over both.

Things to know about OpenCode:

- **No approvals.** It runs its tools without asking; its own `permission`
  config decides what may run, so its sessions have no auto-approve switch.
- **The mesh's agents as tools** are given to it over ACP when each session
  starts, and the note on how to use them with its first message. Nothing is
  written to its config.
- **No taking over.** Its conversations from a terminal are not listed, and
  cannot be continued as a session.
- **In a cage** it reaches OpenCode's own provider through the agent
  (ADR-059): the free models with no key, the paid ones with
  `OPENCODE_API_KEY` from the agent's environment. Its config file is not let
  in, since a provider's key lives in it, and its conversations are the
  cage's own, in a database of its own ([Cages](cages.md)). Sealed cages run
  Claude Code only.

## Changing the agent's flags

The installed unit runs `shrooms-agent` with no flags. To change one,
override `ExecStart` in a drop-in. The empty `ExecStart=` line is required,
because it clears the original:

```
systemctl --user edit shrooms-agent
```

```
[Service]
ExecStart=
ExecStart=/bin/bash -lc 'exec /usr/local/bin/shrooms-agent --meshes home --stt-threads 4'
```

Then restart with `systemctl --user restart shrooms-agent`.

| Flag | Default | What it is for |
|---|---|---|
| `--meshes` | every mesh | A comma-separated list of the meshes to serve on, by label. |
| `--port` | `7387` | The port on each mesh address. The apps and the installer's firewall rule expect 7387. |
| `--state` | `~/.local/share/shrooms-agent` | Where sessions and their history are kept. |
| `--socket` | `/run/shrooms/shrooms.sock` | The shrooms daemon's control socket. |
| `--agent-socket` | `$XDG_RUNTIME_DIR/shrooms-agent/agent.sock` | The agent's own socket, for this user only. |
| `--claude` | `claude` | The Claude Code binary. |
| `--pi` | `pi` | The pi binary. `""` leaves pi out. |
| `--pi-args` | none | Extra arguments for every pi session. |
| `--opencode` | `opencode` | The OpenCode binary. `""` leaves OpenCode out. |
| `--opencode-model` | none | The model an OpenCode session uses when it names none. Empty leaves OpenCode's own config to decide. |
| `--mcp` | `true` | Give every session the mesh's agents as MCP tools and tell it how to use them ([Agents together](together.md)). |
| `--cages` | `true` | Offer cages when podman is installed. |
| `--cage-image` | the workbench | The image cages are made from, for the whole machine. |
| `--sealed-token` | `sealed-claude-token` in the state directory | The file with the Claude Code token sealed cages run on ([Cages](cages.md)). |
| `--stt-model` | `~/.local/share/whisper/ggml-parakeet-tdt-0.6b-v3-q4_k.bin` | The speech-to-text model for voice notes. |
| `--stt-bin` | from the model | `parakeet-cli` for a Parakeet model, otherwise `whisper-cli`. |
| `--stt-threads` | cores, up to 12 | Threads used for transcription. |
| `--uploads-keep` | `336h` | How long files sent to sessions are kept; `0` keeps them for good (ADR-078). |
| `--files-url` | `$SHROOMS_AGENT_FILES_URL` | The mesh's file server, where sessions publish files; empty, the agent serves them itself (ADR-079). The installer's `--files-url` sets the variable. |
| `--schedules` | `~/.config/shrooms-agent/schedules.json` | The sessions' schedules: this machine's Scala and which calendar is whose (ADR-064). None when the file is missing. |
| `--verbose` | `false` | Log Claude Code's stderr as well. |

The agent asks the daemon for its mesh addresses every 30 seconds while it
serves no mesh, and every minute after, and starts serving on a new one by
itself: the machine's first mesh, or another it joins later. No restart is
needed. Run the installer again, though, so the firewall opens the new
address too.

## Voice notes

The apps can record a voice note. It is transcribed on the agent's machine,
so the audio never leaves your devices. Voice notes are on when the agent
finds both the model file and the program that runs it. Otherwise they are
off, and the log at startup says which one was missing.

- **The easy way:** run the installer again with `--voice`. It installs git,
  cmake, a C++ compiler, ffmpeg and curl if they are missing (with apt or
  dnf), builds `parakeet-cli` from a pinned whisper.cpp commit into
  `~/.local/bin`, and downloads the Parakeet v3 model (about 400 MB) into
  `~/.local/share/whisper`. The build is the user's own and stays after
  `--uninstall`.
- **ffmpeg** must be on the service's `PATH`. Every note is converted with
  it first. Only Whisper also uses ffprobe, which comes with ffmpeg.
- **Speed.** Parakeet runs at 6–8 times real time on a recent laptop CPU and
  about real time on a small four-core VPS. A Parakeet note over 90 s is cut
  at pauses into pieces of about a minute, and one is transcribed at
  once for every four threads (three with the default of 12, four from
  `--stt-threads 16`): on a laptop, a 406 s
  note took 40 s instead of 99 s. Set `--stt-threads` to the machine's real
  core count when it shares the CPU with other work.
  [speech-to-text](../speech-to-text.md) has the details.
- **Whisper instead.** Point `--stt-model` at a Whisper ggml model (any file
  without "parakeet" in its name) and the agent runs it with `whisper-cli`.
  Parakeet detects the language itself. Whisper is slower but drops filler
  words. [speech-to-text](../speech-to-text.md) has the measurements.

## Cages

A cage is a session in a rootless podman container of its own. Inside, the
session is root and can install what it needs. Outside, it is your user, and
it sees nothing of your home directory except its project. Use cages for
sessions you want to auto-approve. The whole design is in
[Cages](cages.md) and [the cages research and plan](../agents-in-cages.md).

To get them:

1. Install podman and make sure it works **rootless** for the agent's user
   (`podman run --rm docker.io/library/debian:bookworm-slim true` as that
   user). Rootless podman needs subordinate uid and gid ranges for the user
   in `/etc/subuid` and `/etc/subgid`, which most distributions add when the
   user is created.
2. Restart the agent. Its log says `cages offered`, and "In a cage" appears
   in "+ session" in both apps.
3. Start the first caged session. The agent builds its workbench image (Node,
   git, a C toolchain, Python, pi) on the machine. This takes a few minutes,
   and until it is done the session answers that it is being built. The image
   rebuilds itself when a newer agent brings a changed Containerfile.

Claude Code is not in the image. The machine's own `claude` is mounted
read-only, so a cage always runs the same version as the machine. A machine
with docker and no podman gets no cages.

## On a server

On a machine you share, or one exposed to the internet, run the agent under
an account of its own with no sudo:

1. Create the user (for example, `agent`) and install Claude Code and/or pi
   for it, logged in as described above.
2. Run the installer from an admin account with
   `sudo bash install-agent.sh --user agent`. The installer turns lingering
   on, so the service runs with nobody logged in.
3. To manage the service later, log in as that user properly: `ssh` in as
   `agent`, or use `sudo machinectl shell agent@`. `sudo -u agent systemctl --user`
   fails, because there is no session bus for it.

Since sessions run as `agent`, auto-approve cannot touch the relay or
anything else root owns.

## The apps

Both are published, with updates, in public repositories of their own:

- **Android:** in F-Droid, Settings → Repositories → add
  `https://apps.vpavlin.xyz/fdroid/repo`, then install **Shrooms** (the mesh)
  and **Shrooms Agents**. Open Shrooms Agents from the "agents" link in the
  shrooms app the first time, so it gets the peers.
- **Basecamp:** Package Manager → Repositories → add
  `https://apps.vpavlin.xyz/basecamp/logos-repo.json`, then install
  `shrooms_core`, `shrooms` and `shrooms_agents`. Then let your user read the
  shrooms socket on the desktop (step 3 under "Basecamp" below).

The rest of this section is for building them yourself, from a checkout.

### Android

Shrooms Agents for Android is the "agents" flavour of the shrooms Android
code. It is a separate app (`xyz.vpavlin.shrooms.agents`) and is not a mesh
client itself: the phone joins the mesh with the **shrooms** app, and the
"agents" link in shrooms opens Shrooms Agents and hands it the peers it can
reach. Every agent also lists the mesh as its machine sees it, so knowing one
agent is enough to find the rest. You can also add a machine by name inside
the app.

To build it you need docker, the Android SDK (default `~/Android/Sdk`, or set
`ANDROID_SDK`) with the NDK at `$SDK/ndk/android-ndk-r27c` (or set
`NDK_VER`), and a signing key.

1. **Make the signing key, once.** The app is signed with a key of its own.
   Keep it outside the repository: whoever holds it can ship an update to the
   app that drives your agents. The build expects a PKCS#12 keystore with the
   alias `shrooms-agents`, and a file next to it holding the password:

   ```
   mkdir -p ~/apk-signing/shrooms-agents && cd ~/apk-signing/shrooms-agents
   keytool -genkeypair -storetype pkcs12 -keystore shrooms-agents.keystore \
       -alias shrooms-agents -keyalg RSA -keysize 4096 -validity 10000 \
       -dname "CN=Shrooms Agents"
   printf '%s\n' 'the-password-you-chose' > password && chmod 600 password
   ```

   The build uses the same password for the store and the key. Set
   `AGENTS_KEYS` to keep the key somewhere else.
2. **Build the Go core for Android.** This fetches the arm64 delivery
   library and builds `android/logosvpn.aar` in a container:

   ```
   make aar
   ```

3. **Build the app:**

   ```
   AGENTS=1 VERSION_CODE=1 VERSION_NAME=0.1 scripts/build-apk.sh
   ```

   The result is `android/shrooms-agents.apk`. Raise `VERSION_CODE` for each
   build you want phones to accept as an update.
4. **Install it** with `adb install -r android/shrooms-agents.apk`, or publish
   it to your own F-Droid repository so phones get updates.
5. **Open it from the shrooms app's "agents" link** the first time, so it
   gets the peers.

The app is arm64 only, like the shrooms app.

### Basecamp

The module is `shrooms_agents`, a QML view. It depends on `shrooms_core`,
which does the networking that Basecamp does not allow in a view. Building
needs nix with flakes.

1. **Build both packages:**

   ```
   make basecamp-core-lgx
   make basecamp-agents-lgx
   ```

   They end up in `result-core/` and `result-agents/` as `.lgx` files. These
   are the portable builds, which do not refer to your nix store.
2. **Install them** in Basecamp: Package Manager, Install from file, pick
   the `shrooms_core` package first and then `shrooms_agents`. If you run a
   Basecamp package repository, publish both there instead. A file installed
   by hand does not update itself.
3. **Let your user read the shrooms socket on the desktop.** `shrooms_core`
   reads the daemon's status for this device's mesh addresses and its peers.
   On a desktop where shrooms is installed natively, set
   `socket_group = "your-username"` in `/etc/shrooms/config.toml` and restart
   shrooms. Where the daemon runs in a container, the ACL from
   `install-agent.sh` does the same.

The module finds agents among the peers in the daemon's status, and this
machine's own agent on its socket, with or without a mesh (ADR-056).
Nothing in it needs to be configured.

## Sessions kept running

A session normally sleeps: its process is stopped after a while idle and
started again with the next message, which resumes the conversation. A
session that works on its own (a pi agent with a heartbeat extension, or a
chat bridge that passes on messages from elsewhere) must not sleep. Set
`keep_running` on it, and the agent never stops it as idle, starts it again
if it ends, and starts it when the agent itself starts.

The apps have no switch for this yet. Set it over the API, from any device in
the mesh:

```
curl -X POST "http://[MESH-ADDRESS]:7387/v1/sessions/NAME/settings" \
    -H 'Content-Type: application/json' -d '{"keep_running": true}'
```

You can also set it when you create the session, with `"keep_running": true`
in `POST /v1/sessions`. `--adopt-pi` sets it for the session it creates.

## Updating

- **The agent:** run `sudo bash install-agent.sh` again, with the same
  options as before. It pulls `ghcr.io/vpavlin/shrooms-agent:latest`, replaces
  the binary and restarts the service. The image is separate from
  `shrooms:latest` on purpose, so the shrooms daemon's auto-update never
  changes the agent and an agent release never restarts the daemon.
- **What a restart does:** running session processes end. Conversations are
  kept, and the next message resumes each one. Sessions kept running start
  again by themselves. Any turn in progress at the time is lost.
- **Claude Code and pi** update as they do in a terminal. The agent runs
  whatever version is installed.
- **The apps:** build and install again. Nothing on the machines depends on
  the app version.

## Where things live

Everything belongs to the agent's user. Under the state directory,
`~/.local/share/shrooms-agent` by default:

| Path | What it is |
|---|---|
| `sessions.json` | The sessions: name, directory, harness, settings (auto-approve, keep running, model, skills, meshes, who it takes tasks and files from, cage) |
| `events/NAME.jsonl` | Each session's history as the apps show it |
| `tasks.json` | Tasks between agents ([Agents together](together.md)) |
| `uploads/NAME/` | Files and voice notes sent to a session, kept two weeks (`--uploads-keep`) |
| `published/` | Files sessions published, when there is no `--files-url`; served at `/files/` |
| `archive/NAME/` | A caged session's working directory as a fresh start cleared it, kept 30 days |
| `schedules/NAME.json` | What the schedule runner remembers of a session's calendar |
| `cages/` | What caged sessions are started with, their private harness homes and their sockets, readable only by the owner |

Elsewhere:

- `~/.claude/`: Claude Code's login, settings and transcripts.
- `~/.pi/agent/`: pi's `settings.json`, `models.json`, `mcp.json` and
  `sessions/`.
- `~/.config/opencode/`, `~/.local/share/opencode/`: OpenCode's settings and
  conversations.
- `~/.config/shrooms-agent/schedules.json`: the sessions' schedules, if any.
- `$XDG_RUNTIME_DIR/shrooms-agent/agent.sock`: the agent's socket.
- `~/.local/bin/parakeet-cli`, `~/.local/share/whisper/`: voice notes.
- `~/.config/systemd/user/shrooms-agent.service.d/`: your drop-ins.
- Logs: `journalctl --user -u shrooms-agent`.

`--uninstall` leaves all of these in place. Delete the state directory to
remove the sessions as well.

## Troubleshooting

Start with the log, as the agent's user:

```
journalctl --user -u shrooms-agent -b
```

**The log says "no mesh yet: serving this machine only, on its socket".**
Basecamp on this machine reaches the agent; nothing else does. The `why`
field says why there is no mesh:

- `permission denied`: the user cannot read the daemon's socket. Run the
  installer again, which repairs the ACL. If it happened right after a reboot
  or a shrooms update, check that `/etc/tmpfiles.d/shrooms-agent-USER.conf`
  starts with a `d /run/shrooms` line and that the drop-in
  `/etc/systemd/system/shrooms.service.d/20-agent-access.conf` exists. Both
  are needed, because `/run/shrooms` is created anew whenever the daemon
  starts.
- `no such file`, or `connection refused`: shrooms is not running, or not
  installed.
- `the daemon reports no meshes`: join a mesh.

Once the cause is fixed, the agent finds the mesh within half a minute and
logs `serving agents` with the mesh; it does not need a restart.

**"not serving on the meshes asked for", "this device is not in a mesh
called …".** `--meshes` names a mesh this device is not in, and the agent
serves no mesh until it is. Mesh labels are listed by `shrooms status`.

**Every reply says "Not logged in · Please run /login".** Claude Code is not
logged in for the agent's user. See "Claude Code". Typing `/login` in the app
does not work.

**pi is not offered, or the log has no `harness name=pi` line.** The agent
did not find `pi` on its `PATH`. The same goes for OpenCode and `opencode`. The service starts the agent through a login
shell, which reads `~/.profile` or `~/.bash_profile` but often not
`~/.bashrc`, and most `.bashrc` files stop early when not interactive. pi
installed through nvm or a Node version manager set up in `.bashrc` is
therefore missing. Either set `PATH` in `~/.profile`, or give the full path
with `--pi /home/you/.nvm/versions/node/v22.x/bin/pi`. Check what the service
sees with `bash -lc 'command -v pi claude'`.

The `PATH` is the one the agent started with. pi installed later into a
directory already on it is found within 30 seconds; one whose installer added
a new directory to your profile is not, until the agent restarts:
`systemctl --user restart shrooms-agent`. Before 2026-11-04 the agent looked
for pi only at startup, so on an older agent any pi installed after it needs
that restart.

**pi sessions fail with no model, or a provider's authentication error.** The
key variable is set in your terminal but not in the service. Add an
`EnvironmentFile=` drop-in (see "pi"). The Venice credits display stays
empty for the same reason.

**A machine is shown as "unreachable".** The apps show a machine as
unreachable after it has missed about 25 seconds of polling, and keep it on
the list for a week. Check, in order:

1. The machine is online in the mesh: `shrooms status` on the phone or
   desktop lists it as a peer with a working tunnel.
2. The agent is running: `systemctl --user status shrooms-agent` there.
3. It listens on the address you expect: `curl
   "http://[MESH-ADDRESS]:7387/v1/harnesses"` from another member. If this
   works locally on the machine but not from elsewhere, the firewall is
   dropping it. Run the installer again, which adds the rule for the current
   mesh addresses. Under firewalld, the default zone takes the mesh
   interface, so without the rule port 7387 is closed.
4. The machine joined a new mesh after the agent started: the agent serves
   on it within a minute by itself, but the firewall opens it only once the
   installer has run again.

**The phone app says "No agents found".** It has not been given any peers.
Open it from the "agents" link in the shrooms app, or add a machine by name.

**The Basecamp module shows a socket error.** `shrooms_core` cannot read the
daemon's socket on the desktop. Set `socket_group` or apply the ACL (see
"Basecamp").

**"voice notes off: no speech-to-text model".** The model file is not at
`--stt-model`. Run the installer with `--voice`, or point the flag at your
model. **"voice notes off: its CLI was not found"** means the model is there
but `parakeet-cli` (or `whisper-cli`) is not on the service's `PATH`.
`~/.local/bin` has to be on the `PATH` a login shell sets up.

**No "In a cage" option.** podman is not installed, or `--cages=false` is
set. If caged sessions start and then fail, check that rootless podman works
for the user (see "Cages").

**A restart of the agent does not take effect.** A unit in
`~/.config/systemd/user/shrooms-agent.service` overrides the installed one.
`systemctl --user cat shrooms-agent` shows which file is in use.

## See also

- [Using it](using.md): sessions, approvals and the apps
- [Agents together](together.md): agents asking each other for help
- [Cages](cages.md): sessions in containers
- [the reference](../agents.md): the reference, with the whole API
- [harnesses and logging in](../agents-harnesses.md): logging in, and adding a harness
- [speech-to-text](../speech-to-text.md): the voice note engines, measured
