# Setting up Shrooms Agents

This page takes a machine that already runs shrooms to one that serves its
coding agents to your phone and to Basecamp. It covers the agent on each
machine, Claude Code and pi as the agent runs them, voice notes, cages, and
getting the two apps. The last part is what to check when something does not
connect.

Shrooms Agents has three parts:

- **`shrooms-agent`**, a daemon on each machine that runs coding agents. It
  keeps that machine's sessions (a name, a directory, and Claude Code or pi
  working in it) and serves them over HTTP on the machine's mesh addresses,
  port 7387, and nowhere else.
- **The Android app**, Shrooms Agents, a separate app next to the shrooms app.
- **The Basecamp module**, `shrooms_agents`, which uses `shrooms_core` for its
  networking.

The agent has no logins and no tokens. The mesh is the access control: only
members of your meshes can reach the address it listens on, and every request
is attributed to the device it came from. The reasoning is in
[the reference](../agents.md), "Access: the bind is the access control".

## Before you start

On each machine that will run agents:

- **shrooms is installed, running, and in a mesh.** The agent asks the
  daemon, over `/run/shrooms/shrooms.sock`, for this machine's mesh addresses.
  Without a mesh it has nowhere to listen. `shrooms status` should list at
  least one mesh.
- **Linux with systemd.** The agent runs as a systemd *user* service.
- **docker or podman**, whichever shrooms already uses. The installer gets
  the binary out of an image. Without either, give it a binary with
  `--binary` (see below).
- **A user account that is not root.** A session can do anything its user
  can, so the agent refuses to be installed for root. On a desktop, use your
  own account. On a server, a dedicated account is better (see "On a server").
- **The coding agents themselves, set up as that user.** The installer
  installs neither Claude Code nor pi. Each must work from a terminal as that
  user first: installed, logged in or given its keys, and paid for. The agent
  only runs what is already there.

You need at least one of Claude Code or pi. A machine with neither still runs
the agent, but its sessions have nothing to run.

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
     serving on port 7387: Claude Code,pi
     voice notes off (--voice to build them here)
   done: the Shrooms Agents app and Basecamp module find it on the mesh.
   ```

   It also tells you if Claude Code is missing or not logged in for that
   user. Fix those before you go on (see "Claude Code").

Running the installer again is safe. To update the agent or add `--voice`
later, run it again.

### Installer options

| Option | What it does |
|---|---|
| `--user NAME` | Serve this user's agents instead of the user running sudo. Use it when you are root, or when the agent runs under a dedicated account. |
| `--voice` | Also build whisper.cpp's `parakeet-cli` and download the Parakeet model, checked by sha256, for voice notes. |
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
  7387 to this machine's own mesh addresses only. If neither is active, it
  does nothing.
- **The service.** `/etc/systemd/user/shrooms-agent.service`, enabled and
  started for the user, with lingering turned on (`loginctl enable-linger`)
  so it keeps running while the user is logged out. It starts the agent
  through a login shell (`bash -lc`), so sessions find the same programs a
  terminal would: `claude`, `pi`, `nix`, `go`. "pi not found" under
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

At startup the log shows a `serving agents` line for each mesh, a `harness`
line for pi if pi was found, and either `voice notes on` or
`voice notes off` with the reason.

From any device in the mesh, use the machine's mesh address (shown by
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
agent offers pi when it finds `pi` on its `PATH` at startup, and runs it as
`pi --mode rpc`. pi's model, provider and keys come from pi's own settings,
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
  `~/.pi/agent/mcp.json`. When the agent starts, it adds an entry called
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
| `--claude` | `claude` | The Claude Code binary. |
| `--pi` | `pi` | The pi binary. `""` leaves pi out. |
| `--pi-args` | none | Extra arguments for every pi session. |
| `--mcp` | `true` | Give every session the mesh's agents as MCP tools and tell it how to use them ([Agents together](together.md)). |
| `--cages` | `true` | Offer cages when podman is installed. |
| `--cage-image` | the workbench | The image cages are made from, for the whole machine. |
| `--stt-model` | `~/.local/share/whisper/ggml-parakeet-tdt-0.6b-v3-q4_k.bin` | The speech-to-text model for voice notes. |
| `--stt-bin` | from the model | `parakeet-cli` for a Parakeet model, otherwise `whisper-cli`. |
| `--stt-threads` | cores, up to 12 | Threads used for transcription. |
| `--verbose` | `false` | Log Claude Code's stderr as well. |

The agent reads the mesh addresses once, at startup. If the machine joins
another mesh, restart the agent, and run the installer again so the firewall
opens the new address too.

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
- **ffmpeg and ffprobe** must be on the service's `PATH`. Every note is
  converted with them first.
- **Speed.** Parakeet runs at 6–8 times real time on a recent laptop CPU and
  about real time on a small four-core VPS. Set `--stt-threads` to the
  machine's real core count when it shares the CPU with other work.
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

The module finds agents among the peers in the daemon's status. Nothing in
it needs to be configured.

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
| `sessions.json` | The sessions: name, directory, harness, settings (auto-approve, keep running, cage) |
| `events/NAME.jsonl` | Each session's history as the apps show it |
| `tasks.json` | Tasks between agents ([Agents together](together.md)) |
| `uploads/NAME/` | Files and voice notes sent to a session |
| `cages/` | The environment files of caged sessions, readable only by the owner |

Elsewhere:

- `~/.claude/`: Claude Code's login, settings and transcripts.
- `~/.pi/agent/`: pi's `settings.json`, `models.json`, `mcp.json` and
  `sessions/`.
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

**The log repeats "waiting for the shrooms daemon".** The `why` field
explains it:

- `permission denied`: the user cannot read the socket. Run the installer
  again, which repairs the ACL. If it happened right after a reboot or a
  shrooms update, check that `/etc/tmpfiles.d/shrooms-agent-USER.conf`
  starts with a `d /run/shrooms` line and that the drop-in
  `/etc/systemd/system/shrooms.service.d/20-agent-access.conf` exists. Both
  are needed, because `/run/shrooms` is created anew whenever the daemon
  starts.
- `no such file`, or `connection refused`: shrooms is not running.
- `the daemon reports no meshes yet`: join a mesh.

**"this device is not in a mesh called …".** `--meshes` names a mesh this
device is not in. Mesh labels are listed by `shrooms status`.

**Every reply says "Not logged in · Please run /login".** Claude Code is not
logged in for the agent's user. See "Claude Code". Typing `/login` in the app
does not work.

**pi is not offered, or the log has no `harness name=pi` line.** The agent
did not find `pi` on its `PATH`. The service starts the agent through a login
shell, which reads `~/.profile` or `~/.bash_profile` but often not
`~/.bashrc`, and most `.bashrc` files stop early when not interactive. pi
installed through nvm or a Node version manager set up in `.bashrc` is
therefore missing. Either set `PATH` in `~/.profile`, or give the full path
with `--pi /home/you/.nvm/versions/node/v22.x/bin/pi`. Check what the service
sees with `bash -lc 'command -v pi claude'`.

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
4. The machine joined a new mesh after the agent started: restart the agent.

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
