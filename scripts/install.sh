#!/usr/bin/env bash
# Install shrooms ON THIS MACHINE and start it, from the published image.
#
# One command takes a bare machine to a running mesh member: it fetches the
# image, generates the config, installs a systemd unit and starts it. There is
# no separate "now run the daemon" step.
#
#   sudo ./install.sh prepare --name fedora          # then redeem an invite
#   sudo ./install.sh join <NETWORK-KEY> --name laptop
#   sudo ./install.sh init --relay                   # create a new mesh
#
# `prepare` is the one to use with invites: it installs and starts the daemon
# with no mesh, and the daemon waits. Then, on a machine already on the mesh,
# `shrooms invite` — and back here `sudo shrooms join --invite <TOKEN>`, which
# brings it up without a restart.
#
# Everything after init/join goes straight to shrooms, so its flags are
# whatever that version supports rather than a copy that drifts.
#
# Needs docker or podman, and /dev/net/tun. No Go toolchain, no repository
# checkout, no liblogosdelivery — everything is in the image.
#
# This is the counterpart to scripts/deploy.sh, which pushes to a remote host
# from a machine that has the repo. Here you are already on the box, which is
# the usual case for "I just got a new machine".
#
# What it touches:
#   /etc/shrooms/           config
#   /var/lib/shrooms/       device identity and announce sequence number
#   /run/shrooms/           control socket
#   /usr/local/bin/shrooms  wrapper so `shrooms status` works on the host
#   a shrooms systemd unit, and a container of the same name
#
# Re-running is safe: an existing config and identity are left alone unless
# --force is given. Losing the identity means a new overlay address and looking
# like a different device to every peer, so it is never destroyed by accident.
set -euo pipefail

IMAGE=${IMAGE:-ghcr.io/vpavlin/shrooms:latest}
FORCE=0

usage() {
    cat <<EOF
usage: $0 [--image REF] [--force] (init | join <NETWORK-KEY> | prepare) [flags...]

  init                 create a new mesh and print its key
  join KEY             join an existing mesh with its network key
  prepare              install and wait; join later with an invite
  prepare              write the config with the key left blank, for setting a
                       machine up without the key passing through anyone else

Everything after init/join is passed straight to shrooms, so its flags are
whatever that version supports:

  --name NAME          device name (default: this machine's hostname)
  --relay              also forward for peers that cannot connect directly
  --advertise IP:PORT  public endpoint, if not on a local interface
  --port N             UDP port

This script's own options:
  --image REF          image to run (default: $IMAGE)
  --force              regenerate the config if one exists

Examples:
  sudo $0 prepare --name fedora        # then: sudo shrooms join --invite TOKEN
  sudo $0 join P27KNQ2... --name laptop
  sudo $0 init --relay
EOF
    exit 1
}

# Only this script's own options are parsed here; the first non-option ends it
# and everything from there is the shrooms command line.
#
# Deliberately NOT re-declaring --name/--relay/--advertise: they belong to
# shrooms, and duplicating them means this script silently fails to support
# any flag added there later.
while [ $# -gt 0 ]; do
    case "$1" in
        --image) IMAGE=$2; shift 2 ;;
        --force) FORCE=1; shift ;;
        -h|--help) usage ;;
        --) shift; break ;;
        -*) echo "unknown option $1"; usage ;;
        *) break ;;
    esac
done

[ $# -gt 0 ] || usage
case "$1" in
    init|join|prepare) ;;
    *) echo "expected 'init', 'join' or 'prepare', got '$1'"; usage ;;
esac
SETUP=("$@")

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo $0 ...)"; exit 1; }

echo "==> checking this machine"
# docker or podman. Fedora ships podman and no docker, and everything used here
# — host networking, NET_ADMIN, a device, bind mounts — is spelled identically
# in both. Run as root either way: a rootless container cannot create a TUN in
# the host's namespace, which is the entire job.
RUNTIME=$(command -v docker || command -v podman || true)
[ -n "$RUNTIME" ] || { echo "neither docker nor podman is installed"; exit 1; }
[ -e /dev/net/tun ] || { echo "no /dev/net/tun — the kernel needs the tun module"; exit 1; }
command -v systemctl >/dev/null || { echo "no systemd; see docker/compose-node.yml to run it yourself"; exit 1; }
echo "  $(basename "$RUNTIME") $("$RUNTIME" version --format '{{.Server.Version}}' 2>/dev/null || echo '?'), /dev/net/tun present"

# podman has no daemon to wait for, and ordering after a unit that does not
# exist would hold the service back on every boot.
AFTER="network-online.target"
if [ "$(basename "$RUNTIME")" = docker ]; then
    AFTER="docker.service network-online.target"
fi

# SELinux relabels bind mounts; without :Z the container cannot read its config
# and reports it as missing rather than as a permission problem.
#
# Every mount, including /run. That one was missed, and it is the one that
# fails hardest: the daemon cannot bind its control socket, exits, and systemd
# restarts it forever. "bind: permission denied" on a path root owns reads as
# nonsense until you remember SELinux is in the way.
Z=""
if command -v getenforce >/dev/null && [ "$(getenforce 2>/dev/null)" = "Enforcing" ]; then
    # :z, the shared label — NOT :Z.
    #
    # :Z applies a *private* label, with MCS categories unique to the container
    # that did the relabelling. These directories are touched by more than one
    # container: the short-lived one that writes the config during setup, and
    # the daemon that runs afterwards. With :Z the second gets different
    # categories and is refused access to files the first created, which
    # surfaces as "write config: permission denied" on a file root owns.
    Z=":z"
    echo "  SELinux enforcing, relabelling mounts (shared)"
fi

echo "==> fetching $IMAGE"
"$RUNTIME" pull -q "$IMAGE" >/dev/null || {
    echo "could not pull $IMAGE"
    echo "if the package is private, either make it public or run: $(basename "$RUNTIME") login ghcr.io"
    exit 1
}

mkdir -p /etc/shrooms /var/lib/shrooms /run/shrooms
chmod 700 /etc/shrooms /var/lib/shrooms

# ---------------------------------------------------------------------------
# Config. Generated by the image itself, so this script never needs to know the
# file format.
# ---------------------------------------------------------------------------

if [ -f /etc/shrooms/config.toml ] && [ $FORCE -eq 0 ]; then
    echo "==> config already present, leaving it alone (--force to replace)"
else
    echo "==> generating config (${SETUP[0]})"
    # --hostname so the CLI's own "default: hostname" means THIS machine and not
    # the container's random one. Nothing else needs a name passed.
    #
    # --config/--state are appended, so they win over anything the caller typed:
    # the service unit mounts these paths and nothing else would be read.
    # -i, and the admin directory mounted from the invoking user's home.
    #
    # Both because `shrooms init` mints an authority: it prompts for a
    # passphrase, which a container without a stdin answers with EOF, and it
    # writes the admin key to ~/.config/shrooms — which inside a --rm container
    # is destroyed the moment the command finishes, taking the only key that
    # can ever admit a device to the mesh it has just created.
    #
    # SUDO_USER, not $HOME: this runs under sudo, so $HOME is root's, and the
    # admin key belongs to the person rather than to the machine. The Go side
    # resolves it exactly this way (see defaultAdminDir).
    admin_home=$(getent passwd "${SUDO_USER:-root}" | cut -d: -f6)
    admin_dir=${admin_home:-/root}/.config/shrooms
    mkdir -p "$admin_dir"
    [ -n "${SUDO_USER:-}" ] && chown "$SUDO_USER" "$admin_dir"

    "$RUNTIME" run --rm -i \
        --hostname "$(hostname -s 2>/dev/null || hostname)" \
        -v "/etc/shrooms:/etc/shrooms$Z" \
        -v "/var/lib/shrooms:/var/lib/shrooms$Z" \
        -v "$admin_dir:/root/.config/shrooms$Z" \
        "$IMAGE" "${SETUP[@]}" \
        --config /etc/shrooms/config.toml \
        --state /var/lib/shrooms
    [ -n "${SUDO_USER:-}" ] && chown -R "$SUDO_USER" "$admin_dir" || true
    chmod 600 /etc/shrooms/config.toml
fi

# ---------------------------------------------------------------------------
# Service. A unit wrapping `docker run` rather than compose: compose is a
# separate install, and on podman hosts podman-compose is the least reliable
# part of the stack.
#
# Host networking is deliberate. A VPN node must bind its UDP port on the real
# address — behind docker's bridge the reflexive address peers observe would be
# the gateway's and the source port would be rewritten, so traversal would be
# fighting a layer of NAT that does not exist in reality.
# ---------------------------------------------------------------------------

echo "==> installing the service"
cat > /etc/systemd/system/shrooms.service <<EOF
[Unit]
Description=shrooms overlay mesh
Documentation=https://github.com/vpavlin/shrooms
After=$AFTER
Wants=network-online.target

[Service]
# /run is a tmpfs, so the socket directory has to be recreated on every boot
# rather than only at install. systemd owns it and cleans it up on stop.
RuntimeDirectory=shrooms
RuntimeDirectoryMode=0750
ExecStartPre=-$RUNTIME rm -f shrooms
ExecStart=$RUNTIME run --rm --name shrooms \\
    --network host \\
    --cap-add NET_ADMIN \\
    --device /dev/net/tun \\
    -v /etc/shrooms:/etc/shrooms$Z \\
    -v /var/lib/shrooms:/var/lib/shrooms$Z \\
    -v /run/shrooms:/run/shrooms$Z \\
    $IMAGE daemon --socket /run/shrooms/shrooms.sock
# `systemctl reload shrooms` re-reads the config for what can change while
# running; the daemon reports the rest as needing a restart.
ExecReload=$RUNTIME kill --signal HUP shrooms
ExecStop=$RUNTIME stop shrooms
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

# So `shrooms status` works on the host without anyone remembering the
# docker incantation.
#
# No --socket appended: the daemon listens on the CLI's own default, and
# appending it would break every subcommand that does not take the flag —
# `shrooms key show` among them.
# The runtime is baked in by the first (unquoted) heredoc; everything after it
# is quoted so that "$@" and the inspect format survive verbatim.
cat > /usr/local/bin/shrooms <<EOF
#!/bin/sh
RUNTIME=$RUNTIME
EOF
cat >> /usr/local/bin/shrooms <<'EOF'
# Thin wrapper: run the CLI inside the running node container.
if ! "$RUNTIME" inspect -f '{{.State.Running}}' shrooms 2>/dev/null | grep -q true; then
    echo "the shrooms container is not running" >&2
    echo "  sudo systemctl status shrooms" >&2
    exit 1
fi
# -i so the commands that prompt work through the wrapper. `shrooms set-key`,
# `shrooms invite` and `shrooms key rotate` all read from a terminal, and
# without this they get EOF and fail in a way that reads as a broken install
# rather than a missing flag.
exec "$RUNTIME" exec -i shrooms shrooms "$@"
EOF
chmod 755 /usr/local/bin/shrooms

systemctl daemon-reload
systemctl enable shrooms >/dev/null 2>&1

# A prepared machine used not to be started, on the grounds that a daemon with
# no key would only fail. That stopped being true: a daemon without a mesh now
# holds the control socket and waits to be told which one it is on, which is
# precisely what an invite needs it to be doing. Leaving it stopped meant
# `shrooms join --invite` had nothing to talk to.
if [ "${SETUP[0]}" = "prepare" ]; then
    if ! systemctl restart shrooms; then
        echo
        echo "the service failed to start:"
        journalctl -u shrooms -n 20 --no-pager || true
        exit 1
    fi
    sleep 2
    cat <<EOF

Installed and running, waiting for a mesh.

On a machine already on one:
  shrooms invite                        # or --mesh <name> if it has several

and back here:
  sudo shrooms join --invite <TOKEN> --name $(hostname)

That brings the mesh up without a restart. A network key still works if you
have one rather than an invite:
  sudo shrooms join <NETWORK-KEY> --name $(hostname)
EOF
    shrooms status 2>/dev/null || true
    exit 0
fi
# Not `enable --now ... || enable`: that swallowed a failed start and left the
# script reporting success over a node that never came up.
if ! systemctl restart shrooms; then
    echo "the service failed to start:"
    journalctl -u shrooms --no-pager -n 20
    exit 1
fi

echo "==> waiting for the node to reach the fleet"
for _ in $(seq 1 12); do
    sleep 5
    shrooms status >/dev/null 2>&1 && break
done

echo
if ! shrooms status 2>&1 | head -12; then
    echo "not answering yet — it may still be connecting."
    echo "  journalctl -u shrooms -f"
fi

cat <<EOF

The daemon is running now, and starts on boot.

  systemctl status shrooms      # is it up
  shrooms status                # who is on the mesh
  shrooms paths                 # why a peer is or is not reachable
  journalctl -u shrooms -f      # follow the log

EOF

# Both hints below are derived from what actually happened, not from what was
# typed: the config is the source of truth, and a --relay that failed to take
# effect should not produce advice implying it did.
if [ "${SETUP[0]}" = "init" ]; then
    KEY=$("$RUNTIME" run --rm -v "/etc/shrooms:/etc/shrooms$Z" "$IMAGE" \
        key show --config /etc/shrooms/config.toml)
    cat <<EOF
This machine created the mesh. Add others with:

  sudo bash install.sh join $KEY

Treat that key as a password: anyone holding it is a member of the mesh.
EOF
fi

# Every node needs its UDP port open, not only a relay.
#
# This used to print only for relays, on the reasoning that a relay is the node
# others dial. That is wrong and it cost somebody a day: two peers need at least
# one of them reachable, and a laptop with a default-deny firewall cannot be
# reached even by a phone on the same wifi. Both ends then sit there announcing
# addresses, visible to each other over the rendezvous plane, with every
# handshake failing — which looks like anything except a closed port.
#
# The command is chosen by what is actually installed rather than guessed from
# the distribution, because a Debian box can be running firewalld and a Fedora
# box nftables directly.
firewall_hint() {
    local port="${1:-51820}"
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi "^Status: active"; then
        echo "  sudo ufw allow $port/udp"
        return
    fi
    if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
        echo "  sudo firewall-cmd --add-port=$port/udp --permanent && sudo firewall-cmd --reload"
        return
    fi
    if command -v nft >/dev/null 2>&1 && nft list ruleset 2>/dev/null | grep -q 'policy drop'; then
        echo "  sudo nft add rule inet filter input udp dport $port accept"
        return
    fi
    if command -v iptables >/dev/null 2>&1 && iptables -S INPUT 2>/dev/null | grep -q '^-P INPUT DROP'; then
        echo "  sudo iptables -I INPUT -p udp --dport $port -j ACCEPT"
        return
    fi
    return 1
}

PORT=$(grep -oE '^listen_port *= *[0-9]+' /etc/shrooms/config.toml 2>/dev/null | grep -oE '[0-9]+' | head -1)
PORT=${PORT:-51820}
if hint=$(firewall_hint "$PORT"); then
    echo
    echo "A firewall is running here. Peers reach this node on UDP $PORT, so open it:"
    echo "$hint"
    echo
    echo "Without it this node can still reach others, but nothing can reach it —"
    echo "which presents as peers that appear and never connect."
fi
