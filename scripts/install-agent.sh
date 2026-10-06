#!/usr/bin/env bash
# Install shrooms-agent ON THIS MACHINE (docs/agents.md): this machine's coding
# agents — Claude Code, pi — served to your other devices over the mesh.
#
#   curl -fsSLO https://raw.githubusercontent.com/vpavlin/shrooms/master/scripts/install-agent.sh
#   sudo bash install-agent.sh            # for the user running sudo
#   sudo bash install-agent.sh --voice    # and voice notes (Parakeet), built here
#
# Needs shrooms installed and running here (scripts/install.sh): the agent
# serves only on this machine's mesh addresses, which it asks the daemon for.
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
#     the Parakeet model, checked by its sha256.
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
VOICE=0
UNINSTALL=0

usage() {
    cat <<EOF
usage: sudo $0 [--user NAME] [--voice] [--image REF | --binary PATH] [--uninstall]

  --user NAME   whose agents to serve (default: the user running sudo)
  --voice       also build parakeet-cli and fetch its model, for voice notes
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
[ -S "$SOCK" ] || { echo "no shrooms daemon here ($SOCK): install shrooms first (scripts/install.sh)"; exit 1; }
[ -n "$(overlays)" ] || { echo "the shrooms daemon reports no mesh yet: join one first"; exit 1; }
echo "  for $USER_NAME, on $(overlays | tr '\n' ' ')"

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
echo "==> letting $USER_NAME read the shrooms socket"
if as_user test -r "$SOCK" -a -w "$SOCK" && as_user test -x /run/shrooms; then
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
        cat > /etc/systemd/system/shrooms.service.d/20-agent-access.conf <<'DROPIN'
# shrooms-agent keeps reading the socket over a restart (install-agent.sh).
[Service]
RuntimeDirectoryPreserve=restart
ExecStartPost=-/bin/sh -c 'systemd-tmpfiles --create /etc/tmpfiles.d/shrooms-agent-*.conf'
DROPIN
        systemctl daemon-reload
    fi
    as_user test -r "$SOCK" || { echo "  still cannot read $SOCK"; exit 1; }
    echo "  by ACL ($TMPFILES)"
fi

echo "==> the firewall"
firewall add

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
ok=0
for _ in $(seq 1 20); do
    for a in $(overlays); do
        curl -s -m 2 -o /dev/null "http://[$a]:$PORT/v1/harnesses" && ok=1 && break 2
    done
    sleep 1
done
[ $ok -eq 1 ] || { echo "  the agent does not answer; see: journalctl --user -u shrooms-agent (as $USER_NAME)"; exit 1; }
a=$(overlays | head -1)
titles=$(curl -s -m 3 "http://[$a]:$PORT/v1/harnesses" | grep -o '"title": *"[^"]*"' | cut -d'"' -f4 | paste -sd, - || true)
echo "  serving on port $PORT: ${titles:-?}"
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
echo "done: the Shrooms Agents app and Basecamp module find it on the mesh."
