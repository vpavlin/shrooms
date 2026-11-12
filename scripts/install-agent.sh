#!/usr/bin/env bash
# Install shrooms-agent ON THIS MACHINE (docs/agents.md): this machine's coding
# agents — Claude Code, pi — served to your other devices over the mesh.
#
#   curl -fsSLO https://raw.githubusercontent.com/vpavlin/shrooms/master/scripts/install-agent.sh
#   sudo bash install-agent.sh            # for the user running sudo
#   sudo bash install-agent.sh --voice    # and voice notes (Parakeet), built here
#
# Shrooms is optional (ADR-056). Without it, the agent serves this machine
# alone, on its own socket, which Basecamp finds. With shrooms installed and
# in a mesh (scripts/install.sh), it also serves on the mesh addresses, to your
# other machines and your phone. Added later? Run this again: it lets the agent
# read the daemon's socket, and the agent takes the mesh up by itself.
#
# What it does, each step the one that was done by hand on the first machines:
#   - takes shrooms-agent out of its image, ghcr.io/vpavlin/shrooms-agent (a
#     binary per architecture and nothing else; no Go toolchain) into
#     /usr/local/bin, with the docker or podman shrooms already uses — or,
#     from the portable package (scripts/build-portable.sh) or --binary, the
#     binary given, with no container runtime at all;
#   - lets the user read the daemon's control socket, if they cannot already:
#     an ACL, kept across reboots by /etc/tmpfiles.d/shrooms-agent-USER.conf —
#     the daemon runs in a container, so its socket_group setting cannot name
#     a host group;
#   - opens TCP 7387 to this machine's mesh addresses only, when firewalld or
#     ufw is running;
#   - installs the user service and starts it for that user, with lingering on
#     so it runs while they are logged out;
#   - with --voice, builds whisper.cpp's parakeet-cli as that user and fetches
#     the Parakeet model, checked by its sha256;
#   - with --adopt-pi NAME, takes over a pi agent that runs on its own (a
#     pi-agent.service keeping pi in tmux, as Jimmy, proteus and scribe did):
#     stops that, gives the agent pi's ~/.pi/agent/.env (its provider keys),
#     and continues pi's newest conversation as session NAME, kept running.
#
# The agent runs as the user, never as root: a session can do what that user
# can. Claude Code and pi are that user's own — logged in, set up and paid for
# as they are in a terminal; this installs neither.
#
# Re-running is safe. --uninstall removes all of the above (not the voice
# build, which is the user's, in ~/.local).
set -euo pipefail

IMAGE=${IMAGE:-ghcr.io/vpavlin/shrooms-agent:latest}
PORT=7387
WHISPER_COMMIT=60c0be6ac8fa71b1a2ae2dd938a31a34a508e774
MODEL=ggml-parakeet-tdt-0.6b-v3-q4_k.bin
MODEL_URL=https://huggingface.co/ggml-org/parakeet-GGUF/resolve/main/$MODEL
MODEL_SHA256=8b205b8b39c6535e153de6fb11c51db46125d45c4f16ba496fe41a0fe71b885e
SOCK=/run/shrooms/shrooms.sock

USER_NAME=${SUDO_USER:-}
BINARY=
# In the portable package the agent sits beside this script.
here=$(cd "$(dirname "$0")" && pwd)
[ -x "$here/bin/shrooms-agent" ] && BINARY=$here/bin/shrooms-agent
VOICE=0 FILES_URL=""
UNINSTALL=0
ADOPT=

usage() {
    cat <<EOF
usage: sudo $0 [--user NAME] [--voice] [--adopt-pi NAME] [--image REF | --binary PATH] [--uninstall]

  --user NAME   whose agents to serve (default: the user running sudo)
  --voice       also build parakeet-cli and fetch its model, for voice notes
  --files-url URL  the mesh's file server, where sessions publish files (publish_file,
                ADR-079): any server that takes an HTTP PUT; without it this agent
                keeps and serves them itself
  --adopt-pi NAME  take over a pi agent kept running on its own (pi-agent.service,
                pi in tmux): its newest conversation becomes session NAME, kept running
  --image REF   the image to take shrooms-agent from (default: $IMAGE)
  --binary PATH install this shrooms-agent instead (default: bin/shrooms-agent
                beside this script, as in the portable package, if there is one)
  --uninstall   remove the agent, its service, socket access and firewall rule
EOF
    exit 1
}

while [ $# -gt 0 ]; do
    case "$1" in
        --user) USER_NAME=$2; shift 2 ;;
        --voice) VOICE=1; shift ;;
        --files-url) FILES_URL=$2; shift 2 ;;
        --adopt-pi) ADOPT=$2; shift 2 ;;
        --image) IMAGE=$2; shift 2 ;;
        --binary) BINARY=$2; shift 2 ;;
        --uninstall) UNINSTALL=1; shift ;;
        -h|--help) usage ;;
        *) echo "unknown argument $1"; usage ;;
    esac
done

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo $0 ...)"; exit 1; }
[ -n "$USER_NAME" ] || { echo "whose agents? run with sudo from that user, or give --user NAME"; exit 1; }
[ "$USER_NAME" != root ] || { echo "not as root: a session can do whatever its user can (--user NAME)"; exit 1; }
UID_N=$(id -u "$USER_NAME" 2>/dev/null) || { echo "no user $USER_NAME"; exit 1; }
HOME_DIR=$(getent passwd "$USER_NAME" | cut -d: -f6)
TMPFILES=/etc/tmpfiles.d/shrooms-agent-$USER_NAME.conf

# systemctl --user for that user, from here. Lingering starts their manager;
# it may take a moment to be there.
as_user() { runuser -u "$USER_NAME" -- "$@"; }
user_systemctl() {
    for _ in $(seq 1 20); do
        [ -S "/run/user/$UID_N/bus" ] && break
        sleep 0.5
    done
    as_user env XDG_RUNTIME_DIR="/run/user/$UID_N" DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$UID_N/bus" \
        systemctl --user "$@"
}

# This machine's mesh addresses, from the daemon: where the agent listens, and
# all the firewall needs to let in.
#
# Only the meshes' own addresses: the status lists every peer's too, and a
# firewall rule or a check made against a peer's address is no use here (the
# first version of this script made both).
overlays() {
    local st
    st=$(curl -s -m 5 --unix-socket "$SOCK" http://unix/status 2>/dev/null) || return 0
    if command -v python3 >/dev/null; then
        printf '%s' "$st" | python3 -c 'import json,sys
for m in json.load(sys.stdin).get("meshes") or []:
    if m.get("overlay"): print(m["overlay"])' 2>/dev/null | sort -u
    else
        # The daemon writes compact JSON, meshes before name.
        printf '%s' "$st" | sed -n 's/.*"meshes":\[\(.*\)\],"name".*/\1/p' |
            grep -o '"overlay":"[^"]*"' | cut -d'"' -f4 | sort -u
    fi
}

firewall() { # add | remove
    local verb=$1 a
    if command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
        for a in $(overlays); do
            local rule="rule family=ipv6 destination address=$a/128 port port=$PORT protocol=tcp accept"
            if [ "$verb" = add ]; then
                firewall-cmd -q --permanent --add-rich-rule="$rule" 2>/dev/null || true
            else
                firewall-cmd -q --permanent --remove-rich-rule="$rule" 2>/dev/null || true
            fi
        done
        firewall-cmd -q --reload
        if [ "$verb" = add ]; then echo "  firewalld: port $PORT open to $(overlays | tr '\n' ' ')only"
        else echo "  firewalld: port $PORT closed again"; fi
    elif command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
        for a in $(overlays); do
            if [ "$verb" = add ]; then ufw allow to "$a" port "$PORT" proto tcp >/dev/null
            else ufw delete allow to "$a" port "$PORT" proto tcp >/dev/null 2>&1 || true; fi
        done
        if [ "$verb" = add ]; then echo "  ufw: port $PORT open to $(overlays | tr '\n' ' ')only"
        else echo "  ufw: port $PORT closed again"; fi
    fi
}

if [ $UNINSTALL -eq 1 ]; then
    echo "==> removing shrooms-agent for $USER_NAME"
    loginctl enable-linger "$USER_NAME" 2>/dev/null || true
    user_systemctl disable --now shrooms-agent 2>/dev/null || true
    rm -f /etc/systemd/user/shrooms-agent.service
    firewall remove
    if [ -f "$TMPFILES" ]; then
        rm -f "$TMPFILES"
        command -v setfacl >/dev/null && setfacl -x "user:$USER_NAME" /run/shrooms "$SOCK" 2>/dev/null || true
    fi
    rm -f /usr/local/bin/shrooms-agent
    echo "  done. Its sessions and their history stay in $HOME_DIR/.local/share/shrooms-agent."
    exit 0
fi

echo "==> checking this machine"
MESH=1
if [ ! -S "$SOCK" ]; then
    MESH=0
    echo "  no shrooms daemon here: the agent will serve this machine only, on its own socket."
    echo "  For your other machines and your phone, install shrooms (scripts/install.sh), then run this again."
elif [ -z "$(overlays)" ]; then
    MESH=0
    echo "  the shrooms daemon is in no mesh yet: this machine only, until it joins one; then run this again."
else
    echo "  for $USER_NAME, on $(overlays | tr '\n' ' ')"
fi
AGENT_SOCK=/run/user/$UID_N/shrooms-agent/agent.sock

# --- the binary --------------------------------------------------------------
if [ -n "$BINARY" ]; then
    echo "==> installing shrooms-agent from $BINARY"
    "$BINARY" -h >/dev/null 2>&1 || "$BINARY" --help >/dev/null 2>&1 || [ $? -le 2 ] ||
        { echo "$BINARY does not run here (another architecture?)"; exit 1; }
    install -m 0755 "$BINARY" /usr/local/bin/shrooms-agent
else
RUNTIME=$(command -v docker || command -v podman || true)
[ -n "$RUNTIME" ] || { echo "neither docker nor podman, and no --binary: shrooms-agent comes out of an image"; exit 1; }
echo "==> taking shrooms-agent from $IMAGE"
# Pulled when it can be; an image only on this machine (a local build being
# tried out) is used as it is.
"$RUNTIME" pull -q "$IMAGE" >/dev/null 2>&1 || "$RUNTIME" image inspect "$IMAGE" >/dev/null 2>&1 ||
    { echo "could not get $IMAGE"; exit 1; }
cid=$("$RUNTIME" create "$IMAGE")
trap '"$RUNTIME" rm -f "$cid" >/dev/null 2>&1 || true' EXIT
tmp=$(mktemp)
"$RUNTIME" cp "$cid:/usr/bin/shrooms-agent" "$tmp" 2>/dev/null ||
    { rm -f "$tmp"; echo "$IMAGE has no /usr/bin/shrooms-agent"; exit 1; }
install -m 0755 "$tmp" /usr/local/bin/shrooms-agent
rm -f "$tmp"
fi
command -v restorecon >/dev/null && restorecon /usr/local/bin/shrooms-agent 2>/dev/null || true

# --- the socket ----------------------------------------------------------------
if [ $MESH -eq 0 ]; then
    : # no daemon's socket to read, and no mesh address to open a port on
elif echo "==> letting $USER_NAME read the shrooms socket"; as_user test -r "$SOCK" -a -w "$SOCK" && as_user test -x /run/shrooms; then
    echo "  it can already"
else
    command -v setfacl >/dev/null || {
        echo "  needs setfacl:"
        { command -v apt-get >/dev/null && apt-get install -y -qq acl >/dev/null; } ||
        { command -v dnf >/dev/null && dnf install -y -q acl >/dev/null; } ||
        { echo "  install the acl package and run this again"; exit 1; }
    }
    # The directory line first, with nothing it would change ("-"): without it
    # the ACL line applies at boot only to a directory that already exists,
    # and it does not — the container makes it later, without the ACL. That
    # is how jimmy-crib's agent lost the socket at its first reboot.
    cat > "$TMPFILES" <<EOF
# shrooms-agent for $USER_NAME reads the shrooms control socket (install-agent.sh).
d /run/shrooms - - - -
a+ /run/shrooms - - - - user:$USER_NAME:rx,default:user:$USER_NAME:rw,default:mask::rw
EOF
    systemd-tmpfiles --create "$TMPFILES"
    setfacl -m "user:$USER_NAME:rw,mask::rw" "$SOCK"
    # And across restarts of shrooms, not only boots. Its unit has
    # RuntimeDirectory=shrooms: systemd deletes /run/shrooms when the service
    # stops and makes it afresh, without the ACL, when it starts — and the
    # image's auto-update restarts it. On 2026-10-05 an update at 18:11 cut
    # the agents on atlas and jimmy-crib off their socket. Keep the directory
    # over a restart, and apply the ACL after every start as well.
    if systemctl cat shrooms.service >/dev/null 2>&1; then
        mkdir -p /etc/systemd/system/shrooms.service.d
        # The socket's own ACL too, once it exists. ExecStartPost runs as
        # soon as the daemon is started, which with the daemon in a container
        # is before it has made the socket: the directory got its default
        # ACL, the socket was made a moment later with its mode applied over
        # the inherited mask, and the agent could not read it. pi5 lost its
        # agent-to-agent list that way after a stop and start (2026-11-09).
        # \$i, written as $i in the unit: systemd substitutes $NAME only as a
        # word of its own, so inside the -c string it reaches the shell as is,
        # and the line still runs when pasted into a shell to test it (pi5/jimmy).
        cat > /etc/systemd/system/shrooms.service.d/20-agent-access.conf <<DROPIN
# shrooms-agent keeps reading the socket over a restart (install-agent.sh).
[Service]
RuntimeDirectoryPreserve=restart
ExecStartPost=-/bin/sh -c 'systemd-tmpfiles --create /etc/tmpfiles.d/shrooms-agent-*.conf; i=0; while [ ! -S $SOCK ] && [ \$i -lt 150 ]; do sleep 0.2; i=\$((\$i+1)); done; setfacl -m user:$USER_NAME:rw,mask::rw $SOCK'
DROPIN
        systemctl daemon-reload
    fi
    as_user test -r "$SOCK" || { echo "  still cannot read $SOCK"; exit 1; }
    echo "  by ACL ($TMPFILES)"
fi

if [ $MESH -eq 1 ]; then
    echo "==> the firewall"
    firewall add
fi

# --- the service ---------------------------------------------------------------
echo "==> the service"
cat > /etc/systemd/user/shrooms-agent.service <<'EOF'
# shrooms-agent: this machine's coding-agent sessions, served to the owner's
# other devices over the mesh (docs/agents.md). Installed by install-agent.sh.
[Unit]
Description=shrooms-agent: coding-agent sessions over the mesh
After=network-online.target

[Service]
# Through a login shell, so sessions find what the user's terminal finds —
# claude, pi, nix, go — rather than a unit's bare PATH.
ExecStart=/bin/bash -lc 'exec /usr/local/bin/shrooms-agent'
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
EOF
loginctl enable-linger "$USER_NAME"
# A unit in the user's own directory wins over this one: say so rather than
# leave a hand-made one quietly in charge.
if [ -f "$HOME_DIR/.config/systemd/user/shrooms-agent.service" ]; then
    echo "  note: $HOME_DIR/.config/systemd/user/shrooms-agent.service overrides the installed unit;"
    echo "        remove it to use this one"
fi

# The file server, as a drop-in of its own: it adds to a machine's other
# drop-ins (pi's keys) rather than rewriting the command line they extend.
if [ -n "$FILES_URL" ]; then
    dropin=$HOME_DIR/.config/systemd/user/shrooms-agent.service.d
    as_user mkdir -p "$dropin"
    printf '[Service]\n# Where sessions publish files (ADR-079); install-agent.sh --files-url.\nEnvironment=SHROOMS_AGENT_FILES_URL=%s\n' "$FILES_URL" |
        as_user tee "$dropin/20-files-url.conf" >/dev/null
    echo "  sessions publish to $FILES_URL"
fi

# --- a pi agent of its own --------------------------------------------------------
# Stopped before the agent starts again: two pi processes on one conversation
# would each write their own branch of it.
if [ -n "$ADOPT" ]; then
    echo "==> taking over the pi agent as session $ADOPT"
    as_user bash -lc 'command -v pi' >/dev/null 2>&1 || { echo "  no pi for $USER_NAME"; exit 1; }
    if user_systemctl cat pi-agent.service >/dev/null 2>&1; then
        user_systemctl disable --now pi-agent.service >/dev/null 2>&1 || true
        echo "  pi-agent.service stopped and disabled"
    fi
    as_user tmux kill-session -t pi-agent 2>/dev/null || true
    dropin=$HOME_DIR/.config/systemd/user/shrooms-agent.service.d
    as_user mkdir -p "$dropin"
    # Added to, not replaced: one written by hand may hold more (a heartbeat's
    # settings).
    if ! grep -qs 'EnvironmentFile=-%h/.pi/agent/.env' "$dropin/10-pi-agent.conf"; then
        [ -f "$dropin/10-pi-agent.conf" ] || printf '[Service]\n' | as_user tee "$dropin/10-pi-agent.conf" >/dev/null
        as_user tee -a "$dropin/10-pi-agent.conf" >/dev/null <<'CONF'
# The pi agent taken over by install-agent.sh --adopt-pi: its provider keys,
# which its own start script sourced. Without them pi has no model.
EnvironmentFile=-%h/.pi/agent/.env
CONF
    fi
    user_systemctl daemon-reload
    user_systemctl restart shrooms-agent
fi

# --- voice notes ---------------------------------------------------------------
if [ $VOICE -eq 1 ]; then
    echo "==> voice notes: parakeet-cli and its model"
    need=()
    for c in git cmake c++ ffmpeg curl; do command -v $c >/dev/null || need+=("$c"); done
    if [ ${#need[@]} -gt 0 ]; then
        echo "  installing what the build needs (${need[*]})"
        if command -v apt-get >/dev/null; then apt-get install -y -qq git cmake build-essential ffmpeg curl >/dev/null
        elif command -v dnf >/dev/null; then dnf install -y -q git cmake gcc-c++ ffmpeg-free curl >/dev/null
        else echo "  install: ${need[*]}, then run this again"; exit 1; fi
    fi
    as_user bash -euc "
        mkdir -p ~/.local/src ~/.local/bin ~/.local/share/whisper
        cd ~/.local/src
        [ -d whisper.cpp ] || git clone -q https://github.com/ggml-org/whisper.cpp
        cd whisper.cpp && git fetch -q && git checkout -q $WHISPER_COMMIT
        cmake -B build -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF -DWHISPER_BUILD_TESTS=OFF >/dev/null
        cmake --build build -j \$(nproc) --target parakeet-cli >/dev/null
        install -m 755 build/bin/parakeet-cli ~/.local/bin/
        m=~/.local/share/whisper/$MODEL
        if [ ! -f \$m ]; then curl -fsSL -o \$m.part $MODEL_URL && mv \$m.part \$m; fi
        echo '$MODEL_SHA256  '\$m | sha256sum -c --quiet || { rm -f \$m; echo '  the model did not match its checksum'; exit 1; }
    "
    echo "  built, and the model checked"
fi

user_systemctl daemon-reload
user_systemctl enable shrooms-agent >/dev/null 2>&1
user_systemctl restart shrooms-agent

# --- what there is ----------------------------------------------------------------
echo "==> checking"
# The agent's own socket, with or without a mesh; then the mesh, if any.
ask() { as_user curl -s -m 3 --unix-socket "$AGENT_SOCK" "http://agent$1"; }
ok=0
for _ in $(seq 1 20); do
    ask /v1/harnesses >/dev/null 2>&1 && ok=1 && break
    sleep 1
done
[ $ok -eq 1 ] || { echo "  the agent does not answer on $AGENT_SOCK; see: journalctl --user -u shrooms-agent (as $USER_NAME)"; exit 1; }
titles=$(ask /v1/harnesses | grep -o '"title": *"[^"]*"' | cut -d'"' -f4 | paste -sd, - || true)
echo "  serving on its socket: ${titles:-?}"
if [ $MESH -eq 1 ]; then
    for _ in $(seq 1 20); do
        for a in $(overlays); do
            curl -s -m 2 -o /dev/null "http://[$a]:$PORT/v1/harnesses" && echo "  and on the mesh, port $PORT" && break 2
        done
        sleep 1
    done
else
    echo "  this machine only: Basecamp's Shrooms Agents finds it here"
fi
if ! as_user bash -lc 'command -v claude' >/dev/null 2>&1; then
    echo "  no Claude Code for $USER_NAME: https://docs.claude.com/en/docs/claude-code"
elif [ ! -f "$HOME_DIR/.claude/.credentials.json" ] && [ -z "$(as_user bash -lc 'echo ${CLAUDE_CODE_OAUTH_TOKEN:-}${ANTHROPIC_API_KEY:-}')" ]; then
    echo "  Claude Code is not logged in for $USER_NAME: run 'claude' as $USER_NAME once, and /login"
fi
if journalctl _UID="$UID_N" _SYSTEMD_USER_UNIT=shrooms-agent.service --since "-2min" --no-pager -o cat 2>/dev/null |
    grep -q "voice notes on"; then
    echo "  voice notes on"
else
    echo "  voice notes off (--voice to build them here)"
fi
if [ -n "$ADOPT" ]; then
    conv=$(as_user bash -c 'ls -t "$HOME"/.pi/agent/sessions/*/*.jsonl 2>/dev/null | head -1')
    id=$(basename "${conv:-x}" .jsonl); id=${id#*_}
    if [ -z "$conv" ]; then
        echo "  no pi conversation found: session $ADOPT not made"
    elif curl -s -m 5 "http://[$a]:$PORT/v1/sessions" | grep -q "\"name\": *\"$ADOPT\""; then
        echo "  session $ADOPT is there already"
    else
        code=$(curl -s -m 30 -o /dev/null -w '%{http_code}' -X POST "http://[$a]:$PORT/v1/sessions" -H 'Content-Type: application/json' \
            -d "{\"name\":\"$ADOPT\",\"harness\":\"pi\",\"resume\":\"$id\",\"keep_running\":true}")
        [ "$code" = 201 ] && echo "  session $ADOPT continues $(basename "$conv"), kept running" ||
            echo "  could not make session $ADOPT ($code)"
    fi
fi
if [ $MESH -eq 1 ]; then
    echo "done: the Shrooms Agents app and Basecamp module find it on the mesh."
else
    echo "done: Basecamp's Shrooms Agents finds it on this machine. For your phone and other machines, add Shrooms and run this again."
fi
