#!/usr/bin/env bash
# Install shrooms ON THIS MACHINE and start it, from the published image.
#
# One command takes a bare machine to a running mesh member: it fetches the
# image, generates the config, installs a systemd unit and starts it. There is
# no separate "now run the daemon" step.
#
#   sudo ./install.sh prepare --name fedora          # then redeem an invite
#   sudo ./install.sh init --relay                   # create a new mesh
#
# `prepare` is the one to use with invites: it installs and starts the daemon
# with no mesh, and the daemon waits. Then, on a machine already on the mesh,
# `shrooms invite` — and back here `sudo shrooms join <TOKEN>`, which
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
#   /usr/local/lib/shrooms/ the host-side resolver registration, where a
#                           container cannot reach systemd-resolved itself
#   a shrooms systemd unit, a shrooms-resolved unit, and a container
#
# Re-running is safe: an existing config and identity are left alone unless
# --force is given. Losing the identity means a new overlay address and looking
# like a different device to every peer, so it is never destroyed by accident.
#
# To undo all of it, scripts/uninstall.sh — `--purge` for the config and
# identity as well, which is what "test this again on a clean machine" means.
set -euo pipefail

IMAGE=${IMAGE:-ghcr.io/vpavlin/shrooms:latest}
FORCE=0

# What `prepare` writes where the network key goes, and therefore how both this
# script and the registrar it installs tell a prepared machine from a member.
# state.KeyPlaceholder in the Go, pinned to this file by a test in
# internal/state so the two cannot drift apart unnoticed.
PLACEHOLDER=PASTE-THE-NETWORK-KEY-HERE

usage() {
    cat <<EOF
usage: $0 [--image REF] [--force] (init | prepare) [flags...]

  init                 create a new mesh
  prepare              install and wait: the config is written with no key,
                       so the machine is set up without the key passing
                       through anybody, and joins later with an invite

Everything after init/join is passed straight to shrooms, so its flags are
whatever that version supports:

  --name NAME          device name (default: this machine's hostname)
  --relay              also forward for peers that cannot connect directly
  --advertise IP:PORT  public endpoint, if not on a local interface
  --port N             UDP port

This script's own options:
  --image REF          image to run (default: $IMAGE)
  --force              regenerate the config if one exists. Not needed to turn
                       a prepared machine into the first one: init mints into
                       the config prepare wrote

Examples:
  sudo $0 prepare --name fedora        # then: sudo shrooms join TOKEN
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

# This script's own options, typed after the verb.
#
# The loop above stops at the first non-option, so `install.sh init --force`
# leaves FORCE at 0 and hands --force to shrooms, which has no such flag on any
# verb. Both halves of that are silent: the config-present branch returns before
# the setup container ever runs, so nothing rejects the flag, and the message
# printed is "config already present (--force to replace)" — to somebody who has
# just typed --force. Say where it goes instead of guessing what was meant.
MISPLACED=()
REST=()
i=0
while [ $i -lt ${#SETUP[@]} ]; do
    case "${SETUP[$i]}" in
        --force) MISPLACED+=("--force") ;;
        # --image carries its value with it, or the suggestion below would put
        # the reference where the verb belongs.
        --image) MISPLACED+=("--image" "${SETUP[$((i + 1))]:-}"); i=$((i + 1)) ;;
        *) REST+=("${SETUP[$i]}") ;;
    esac
    i=$((i + 1))
done
if [ ${#MISPLACED[@]} -gt 0 ]; then
    echo "${MISPLACED[0]} is this script's option rather than one of shrooms', so it goes"
    echo "before the verb. You want:"
    echo
    echo "  sudo $0 ${MISPLACED[*]} ${REST[*]}"
    exit 1
fi

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

# WHICH of the two it is, asked of the binary rather than read off its name.
# The podman-docker package installs /usr/bin/docker as a shim over podman, and
# that is what a Fedora box with "docker" on it usually has — so the filename
# said docker, the unit was ordered after a docker.service that does not exist,
# and the line below claimed a docker version that came out of podman.
KIND=docker
if "$RUNTIME" --version 2>/dev/null | grep -qi podman; then
    KIND=podman
fi
echo "  $KIND $("$RUNTIME" version --format '{{.Server.Version}}' 2>/dev/null || echo '?'), /dev/net/tun present"

# podman has no daemon to wait for, and ordering after a unit that does not
# exist would hold the service back on every boot.
AFTER="network-online.target"
if [ "$KIND" = docker ]; then
    AFTER="docker.service network-online.target"
fi

# SELinux relabels bind mounts; without a label the container cannot read its
# config and reports it as missing rather than as a permission problem.
#
# Every mount, including /run. That one was missed, and it is the one that
# fails hardest: the daemon cannot bind its control socket, exits, and systemd
# restarts it forever. "bind: permission denied" on a path root owns reads as
# nonsense until you remember SELinux is in the way.
#
Z=""
if [ -e /sys/fs/selinux/enforce ]; then
    # :z, the shared label — NOT :Z.
    #
    # :Z applies a *private* label, with MCS categories unique to the container
    # that did the relabelling. These directories are touched by more than one
    # container: the short-lived one that writes the config during setup, and
    # the daemon that runs afterwards. With :Z the second gets different
    # categories and is refused access to files the first created, which
    # surfaces as "write config: permission denied" on a file root owns.
    Z=":z"
    echo "  SELinux enabled, relabelling mounts (shared)"
fi

# The image is :latest by default, and that is a trust decision worth naming
# rather than dressing up. Pinning a digest here would look stronger and buy
# little: this script is fetched from the same GitHub account that publishes the
# image, so whoever could substitute one could substitute the other. What
# genuinely helps is knowing WHICH image you got, so set IMAGE to a digest
# (ghcr.io/vpavlin/shrooms@sha256:...) when you want a build that cannot move
# under you, and read the digest printed below when you do not.
echo "==> fetching $IMAGE"
"$RUNTIME" pull -q "$IMAGE" >/dev/null || {
    echo "could not pull $IMAGE"
    echo "if the package is private, either make it public or run: $(basename "$RUNTIME") login ghcr.io"
    exit 1
}

digest=$("$RUNTIME" image inspect --format '{{index .RepoDigests 0}}' "$IMAGE" 2>/dev/null || true)
[ -n "$digest" ] && echo "  running $digest"

mkdir -p /etc/shrooms /var/lib/shrooms /run/shrooms
chmod 700 /etc/shrooms /var/lib/shrooms

# ---------------------------------------------------------------------------
# Config. Generated by the image itself, so this script never needs to know the
# file format.
# ---------------------------------------------------------------------------

# A config that `prepare` wrote, on a machine that turns out to be the first.
#
# `shrooms init` mints INTO such a config, keeping the name, port, mode and
# relay setting already chosen, and refuses one that is already on a mesh. So
# the thing to do here is run init and let it decide — not skip it because a
# file exists, which is what left somebody with a prepared machine and no way
# to create a mesh on it.
#
# Deliberately not deleting the config first. That was the obvious way to stop
# init refusing, and it throws away exactly what init now preserves.
if [ -f /etc/shrooms/config.toml ] && [ $FORCE -eq 0 ] &&
   ! { [ "${SETUP[0]}" = init ] && grep -q "$PLACEHOLDER" /etc/shrooms/config.toml; }; then
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

    # An admin key from a mesh minted here before.
    #
    # `init` refuses it, and is right to — the admin key set is fixed at mint,
    # so a second one is a DIFFERENT mesh. But it refuses in the container's
    # words: "/root/.config/shrooms/admin.json already exists", a path that does
    # not exist on this machine, when what it means is the directory below,
    # mounted there. Said here, where the host path is known.
    #
    # It is the ordinary way to meet this: `uninstall.sh --purge` keeps the admin
    # key on purpose, so re-installing after one lands exactly here.
    admin_check=yes
    for a in "${SETUP[@]}"; do
        case "$a" in
            # --mesh mints admin-<label>.json, --admin-dir looks elsewhere, and
            # --no-admin mints nothing at all.
            --no-admin|--mesh|--mesh=*|--admin-dir|--admin-dir=*) admin_check=no ;;
        esac
    done
    if [ "${SETUP[0]}" = init ] && [ "$admin_check" = yes ] && [ -e "$admin_dir/admin.json" ]; then
        cat <<EOF

$admin_dir/admin.json is the authority of a mesh minted here before.
Minting another would create a DIFFERENT mesh — the mesh id is the hash of its
admin keys — so init stops rather than quietly replacing it.

If that mesh is still alive, keep the file. This machine rejoins it with an
invite from a device that is still a member, not by minting again:
  sudo bash $0 prepare --name $(hostname -s 2>/dev/null || hostname)

If it is finished and you are starting over, the key is what ends it:
  rm $admin_dir/admin.json
EOF
        exit 1
    fi

    # The same two things the wrapper gives an admin-key command, because this
    # is one: a pty when there is one to give, so the passphrase prompt does not
    # echo, and the card reader, without which `init --keycard` cannot reach the
    # authority it is being told to use. The wrapper grew both and this did not,
    # which made the same command behave differently depending on which of the
    # two ran it.
    TTY=
    [ -t 0 ] && [ -t 1 ] && TTY=-t
    CARD=
    [ -S /run/pcscd/pcscd.comm ] && CARD="-v /run/pcscd:/run/pcscd$Z"

    "$RUNTIME" run --rm -i $TTY \
        --hostname "$(hostname -s 2>/dev/null || hostname)" \
        -v "/etc/shrooms:/etc/shrooms$Z" \
        -v "/var/lib/shrooms:/var/lib/shrooms$Z" \
        -v "$admin_dir:/root/.config/shrooms$Z" \
        $CARD \
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
# The runtime, image and SELinux suffix are baked in by the first (unquoted)
# heredoc; everything after it is quoted so that "$@" and the inspect format
# survive verbatim.
cat > /usr/local/bin/shrooms <<EOF
#!/bin/sh
RUNTIME=$RUNTIME
IMAGE=$IMAGE
Z=$Z
EOF
cat >> /usr/local/bin/shrooms <<'EOF'
# Thin wrapper, with two paths — because one of them must not run inside the
# daemon container.
#
# Anything that touches the mesh authority — `init`, `invite`, `admin`,
# `keycard`, `mesh rename` — reads or writes ~/.config/shrooms, and the daemon
# container has no such directory: the unit mounts the config, the state and the
# socket, and nothing of yours. `exec` cannot add a mount, so those commands run
# in a SIBLING container with the same mounts plus the invoking user's admin
# directory. Without it `shrooms init` wrote the only key that can ever admit a
# device into a --rm container, where the next restart destroyed it, and
# `shrooms invite` reported a missing admin key on the machine that holds it.
#
# The daemon container is deliberately NOT given that mount. It has never needed
# the admin key — membership is a credential it verifies, not one it signs — and
# a process that cannot read a key cannot leak it (ADR-025).
needs_admin_key=no
case "${1:-}" in
    init|invite|admin|keycard) needs_admin_key=yes ;;
    # This subcommand only: renaming a mesh MOVES the admin key file with it.
    mesh) [ "${2:-}" = rename ] && needs_admin_key=yes ;;
esac

# Can this user see the daemon's container at all?
#
# Asked before anything else, because the honest answer is not "it is not
# running". The daemon's container belongs to root, and rootless podman is a
# separate store that cannot see root's containers — so on a Fedora box, where
# podman is what `docker` runs, every command here needs sudo and the old
# message for that was "the shrooms container is not running", on a machine
# where systemctl says it plainly is.
#
# Root goes ahead either way. Anyone else only if the runtime can already see
# the container, which is what a working docker group looks like.
#
# One inspect answers both questions asked below: empty output means the runtime
# cannot see a container of that name at all, and anything else is its running
# state. Asking twice cost a second podman inspect — a few hundred milliseconds
# — on every command the wrapper exists to make convenient.
running=$("$RUNTIME" inspect -f '{{.State.Running}}' shrooms 2>/dev/null || true)

if [ "$(id -u)" != 0 ] && [ -z "$running" ]; then
    echo "cannot see the shrooms container as this user." >&2
    echo >&2
    echo "The daemon's container belongs to root. With podman yours is a separate" >&2
    echo "store that cannot see root's at all, so this needs sudo:" >&2
    echo "  sudo shrooms $*" >&2
    exit 1
fi

if [ "$needs_admin_key" = yes ]; then
    # SUDO_USER, not $HOME: this is normally run under sudo, so $HOME is root's
    # while the admin key belongs to the person. Resolved here and mounted at
    # /root/.config/shrooms, because inside the container there is no SUDO_USER
    # and that is where the CLI's own default lands.
    admin_home=$(getent passwd "${SUDO_USER:-$(id -un)}" 2>/dev/null | cut -d: -f6)
    admin_dir=${admin_home:-$HOME}/.config/shrooms
    mkdir -p "$admin_dir"
    [ -n "${SUDO_USER:-}" ] && chown "$SUDO_USER" "$admin_dir"

    # -t as well as -i when both ends are a terminal, so the passphrase prompts
    # read from a pty and do not echo. Omitted when either end is a pipe, where
    # it would put carriage returns through the output.
    TTY=
    [ -t 0 ] && [ -t 1 ] && TTY=-t

    # --uts=host rather than --hostname: `init` defaults the device name to the
    # hostname, and that has to be this machine's rather than a container id.
    # --hostname is refused alongside host networking, which the node needs.
    #
    # A card reader too, when pcscd is running here: `keycard` and the --keycard
    # admin commands talk to it over that socket and it is not in the image.
    CARD=
    [ -S /run/pcscd/pcscd.comm ] && CARD="-v /run/pcscd:/run/pcscd$Z"

    "$RUNTIME" run --rm -i $TTY \
        --network host \
        --uts host \
        -v /etc/shrooms:/etc/shrooms$Z \
        -v /var/lib/shrooms:/var/lib/shrooms$Z \
        -v /run/shrooms:/run/shrooms$Z \
        -v "$admin_dir:/root/.config/shrooms$Z" \
        $CARD \
        "$IMAGE" "$@"
    rc=$?
    # What was just minted belongs to whoever ran sudo, not to root.
    if [ -n "${SUDO_USER:-}" ]; then chown -R "$SUDO_USER" "$admin_dir" 2>/dev/null || true; fi
    exit $rc
fi

# Everything else runs in the daemon container, which is where the mesh is. That
# one has to be up, and by here we know the answer means what it says: a user who
# could not see it at all was turned away above. The admin-key commands are not
# held to this — minting a mesh, or reading `admin show`, is worth having on a
# machine whose daemon is down.
if [ "$running" != true ]; then
    echo "the shrooms container is not running" >&2
    echo "  sudo systemctl status shrooms" >&2
    exit 1
fi
# -i so the commands that prompt work through the wrapper. `shrooms join` and
# `shrooms key rotate` both read from a terminal, and without this they get EOF
# and fail in a way that reads as a broken install rather than a missing flag.
exec "$RUNTIME" exec -i shrooms shrooms "$@"
EOF
chmod 755 /usr/local/bin/shrooms

# ---------------------------------------------------------------------------
# Names, which the daemon cannot arrange for itself here.
#
# The daemon serves DNS for the mesh and then tells the host's resolver to ask
# it — by running `resolvectl`, which is not in the image and is not going to
# be: it belongs to systemd, and the image has no systemd. So on a container
# install that second half has always failed, the daemon logged a warning, and
# `ping6 nas.mesh` said "Name or service not known" beside a status page showing
# a perfectly healthy mesh.
#
# Done from the host instead, where resolvectl lives. The container runs with
# host networking, so the tun interface and the address the resolver listens on
# are both in this namespace already — the same two commands the daemon would
# have run, run by something that can.
#
# NOT by mounting the host's D-Bus socket into the container, which is the other
# way to make resolvectl work in there. That would hand a VPN daemon the whole
# system bus to register a domain, and the line ADR-025 draws about what the
# daemon can reach is worth more than the convenience.
RESOLVED=0
if command -v resolvectl >/dev/null 2>&1; then
    RESOLVED=1
    echo "==> installing the resolver registration"
    install -d /usr/local/lib/shrooms
    # Unquoted heredoc for the one value that has to come from here, then a
    # quoted body so the rest survives verbatim. Same trick as the wrapper.
    cat > /usr/local/lib/shrooms/register-dns <<EOF
#!/bin/sh
PLACEHOLDER=$PLACEHOLDER
EOF
    cat >> /usr/local/lib/shrooms/register-dns <<'EOF'
# Point this host's systemd-resolved at the mesh resolver the daemon runs.
#
# Run by shrooms-resolved.service, which watches rather than fires once. See the
# comment in scripts/install.sh for why this is not done inside the container.
set -eu

# Beside the control socket: the unit mounts /run/shrooms into the container, so
# it is the one directory both sides can see. The daemon reads this file to know
# that registration happened after all, and reports names as working rather than
# warning about something that has already been dealt with.
MARKER=/run/shrooms/resolver-registered

# By path, not by PATH: this runs from a unit, and a service manager's idea of
# where to look for a binary is not the shell's.
SHROOMS=${SHROOMS:-/usr/local/bin/shrooms}
CONFIG=${CONFIG:-/etc/shrooms/config.toml}
INTERVAL=${INTERVAL:-30}

# What root is willing to hand to resolvectl, defined once. Both readers of the
# marker and the value freshly parsed out of the container go through it.
valid_iface() {
    case "$1" in
        ''|*[!A-Za-z0-9._-]*) return 1 ;;
    esac
}

# The interface this host was registered for, left in $iface. `read`, not
# `cat`: this is the most-executed line in the script, and a builtin costs no
# fork where a subshell and /bin/cat cost two.
marker_iface() {
    [ -f "$MARKER" ] || return 1
    read -r iface < "$MARKER" || return 1
    valid_iface "$iface"
}

revert() {
    # resolved forgets a link when its interface disappears, which is the usual
    # case — but a daemon stopped while the tun survives would leave the host
    # pointing at a resolver that has gone. The interface is read back from the
    # marker because by now there may be no container left to ask.
    if marker_iface; then
        rm -f "$MARKER"
        resolvectl revert "$iface" >/dev/null 2>&1 || true
    else
        rm -f "$MARKER"
    fi
}

# Already done, and still true. Cheap on purpose: no container is touched, so a
# tick of the watch loop costs one fork and one D-Bus call while everything is
# working — `case` rather than a pipe into grep, for the same reason.
registered() {
    marker_iface || return 1
    case $(resolvectl status "$iface" 2>/dev/null) in
        *"DNS Servers"*) ;;
        *) return 1 ;;
    esac
}

# Is there a mesh to register names for? Read off the config rather than asked
# of the daemon, so a machine sitting prepared for a week — waiting for an
# invite that has not arrived — is not running `podman exec` twice a minute to
# be told the same thing.
has_mesh() {
    [ -f "$CONFIG" ] || return 1
    ! grep -q "$PLACEHOLDER" "$CONFIG"
}

# One message per reason, not one per attempt. In watch mode the same complaint
# every 30s buries the journal and teaches everyone to ignore this unit.
last=
say() {
    [ "$1" = "$last" ] && return 0
    last=$1
    echo "$1" >&2
}

register() {
    # Compacted as it arrives, so one spelling of the document is in scope
    # rather than two. `shrooms status --json` pretty-prints, so it says
    # `"dns": {` and `"serving": true` — with spaces. Matching the compact
    # spelling found nothing, and since ${var#pattern} returns the string
    # unchanged when the pattern is absent, that failure came out of this
    # script as "the daemon is not serving names" on a machine whose names were
    # fine.
    #
    # Safe to strip blindly because the three values read below cannot contain
    # whitespace, and each is validated before it is used.
    json=$("$SHROOMS" status --json 2>/dev/null | tr -d ' \t\n') || json=
    if [ -z "$json" ]; then
        say "the daemon is not answering yet"
        return 1
    fi

    case "$json" in
        *'"dns":{'*) ;;
        *) say "no dns block in \`shrooms status --json\`; cannot register"; return 1 ;;
    esac

    # The dns object holds only scalars, so the first closing brace ends it and
    # nothing here needs a JSON parser that the host may not have.
    dns=${json#*\"dns\":\{}
    dns=${dns%%\}*}
    case "$dns" in
        *'"serving":true'*) ;;
        *) say "the daemon is not serving names yet"; return 1 ;;
    esac

    # Parameter expansion throughout, and no forked sed or grep: `#` strips the
    # shortest leading match, so each of these takes the FIRST occurrence.
    #
    # The interface is read out of the meshes array rather than the whole
    # document, so it is the primary mesh's — the one the daemon registers —
    # because it is first in that array, not because of where Go happens to
    # declare the field. Taking it from the document meant any earlier key
    # named "interface" would have pointed root's resolvectl at another link.
    addr=${dns#*\"address\":\"};   addr=${addr%%\"*}
    suffix=${dns#*\"suffix\":\"};  suffix=${suffix%%\"*}
    meshes=${json#*\"meshes\":\[}
    iface=${meshes#*\"interface\":\"}; iface=${iface%%\"*}

    # Validated before being handed to resolvectl, because this runs as root and
    # these three values came out of a container. Rejecting is the right
    # failure: names not working is recoverable, and root running an arbitrary
    # string is not.
    valid_iface "$iface" || { say "refusing odd interface: $iface"; return 1; }
    case "$addr"   in ''|*[!0-9A-Fa-f:.]*)   say "refusing odd address: $addr";    return 1 ;; esac
    case "$suffix" in ''|*[!A-Za-z0-9.-]*)   suffix=internal ;; esac

    # Both suffixes, because the resolver answers both: the configured one and
    # the legacy `.mesh`, kept answerable so a change of default does not break
    # every ssh config on the same day. Registering one leaves the other dead.
    set -- "~$suffix"
    [ "$suffix" = mesh ] || set -- "$@" "~mesh"

    resolvectl dns "$iface" "$addr"
    resolvectl domain "$iface" "$@"

    printf '%s\n' "$iface" > "$MARKER"
    last=
    echo "mesh names resolve here: $iface -> $addr, domains $*"
}

# Registration is not a thing that happens once.
#
# `shrooms join` hands the token to the waiting daemon, which re-executes itself
# into the mesh it just joined — same pid, and systemd sees no exit. So nothing
# restarts this unit at the moment names first become answerable, and a machine
# set up with `prepare` (every device after the first) ended up with a working
# mesh and no names, which is where this started. Reconciling on a loop covers
# that, a later in-place restart, and resolved forgetting a link, none of which
# announce themselves.
# How long until the next look. INTERVAL while things are moving, doubling up
# to a ceiling while they are not — because "cannot register" is a state a
# machine can sit in permanently. A daemon that failed to bind port 53 serves
# no names for its whole life, and retrying that every thirty seconds is two
# `podman exec`s a minute, forever, to be told the same thing. Reset on
# success, so a machine that recovers is prompt again.
wait_for=$INTERVAL
MAX_WAIT=${MAX_WAIT:-600}

backoff() {
    wait_for=$((wait_for * 2))
    [ $wait_for -gt $MAX_WAIT ] && wait_for=$MAX_WAIT
    return 0
}

reconcile() {
    if registered; then
        wait_for=$INTERVAL
        return 0
    fi
    # No mesh yet is not a failure and does not back off: the config gate costs
    # one grep and never wakes the container, and the moment an invite lands we
    # want to be looking.
    has_mesh || {
        say "no mesh in $CONFIG yet; nothing to register"
        wait_for=$INTERVAL
        return 0
    }
    if register; then
        wait_for=$INTERVAL
    else
        backoff
    fi
}

command -v resolvectl >/dev/null 2>&1 || exit 0

case "${1:-}" in
    --revert)
        revert
        ;;
    --watch)
        while :; do
            reconcile
            sleep "$wait_for"
        done
        ;;
    *)
        # One pass, for running it by hand. The unit uses --watch; this is what
        # you want when you are standing in front of a machine asking why names
        # are not resolving, and it is what the comments above describe.
        reconcile
        ;;
esac
EOF
    chmod 755 /usr/local/lib/shrooms/register-dns

    # A separate unit rather than ExecStartPost on shrooms.service: this never
    # returns, and ExecStartPost would hold the main unit in `activating` for as
    # long as it ran. PartOf means a restart of the daemon restarts it too,
    # which matters because the tun is new each time and resolved forgets the
    # old one.
    cat > /etc/systemd/system/shrooms-resolved.service <<'EOF'
[Unit]
Description=shrooms mesh names, in systemd-resolved
Documentation=https://github.com/vpavlin/shrooms
After=shrooms.service
PartOf=shrooms.service

[Service]
# Type=simple and a loop, not a oneshot. The moment names become answerable —
# a `shrooms join` completing — produces no systemd event at all, because the
# daemon re-executes itself in place. Something has to keep looking.
Type=simple
ExecStart=/usr/local/lib/shrooms/register-dns --watch
ExecStop=/usr/local/lib/shrooms/register-dns --revert
# always, not on-failure: this is a supervisor now, and the loop only ends by
# being killed. If it dies, something took it down and names stop working.
Restart=always
RestartSec=10

[Install]
WantedBy=shrooms.service
EOF
else
    echo "==> no resolvectl here, so mesh names will need /etc/hosts"
    echo "    sudo shrooms hosts | sudo tee -a /etc/hosts"
fi

systemctl daemon-reload
systemctl enable shrooms >/dev/null 2>&1
# The flag, not a stat of the file: a unit left behind by a previous install on
# a machine whose resolvectl has since gone would answer yes to the stat, and be
# enabled to run a helper that now exits immediately.
if [ $RESOLVED -eq 1 ]; then
    systemctl enable shrooms-resolved >/dev/null 2>&1
fi

# A prepared machine used not to be started, on the grounds that a daemon with
# no key would only fail. That stopped being true: a daemon without a mesh now
# holds the control socket and waits to be told which one it is on, which is
# precisely what an invite needs it to be doing. Leaving it stopped meant
# `shrooms join` had nothing to talk to.
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
  sudo shrooms join <TOKEN> --name $(hostname)

That brings the mesh up without a restart. A network key still works if you
have one rather than an invite:
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

To remove everything this installed, fetch uninstall.sh from beside this script:

  sudo bash uninstall.sh            # the software; this device stays a member
  sudo bash uninstall.sh --purge    # config, identity and image too

--purge lists what it found here and asks before removing any of it. It never
takes the admin key — losing that ends the mesh — so it is safe on the machine
that minted one.

EOF

# Both hints below are derived from what actually happened, not from what was
# typed: the config is the source of truth, and a --relay that failed to take
# effect should not produce advice implying it did.
if [ "${SETUP[0]}" = "init" ]; then
    cat <<'EOF'
This machine created the mesh. Add another device with an invite:

  sudo shrooms invite

That prints a token good for fifteen minutes and a QR code for the phone. The
token admits one device, and an admin-signed credential decides membership
afterwards — which is why the network key is not printed here for you to paste.

There is no longer anything to paste it into: `shrooms join <KEY>` and
`shrooms set-key` were removed with the rest of that path. `shrooms key show`
still reads the key out for recovery, deliberately.
EOF
fi

# Firewall advice for the machine in front of us.
#
# Printed for every node, not only relays. That distinction was made once, on
# the reasoning that a relay is the node others dial, and it cost somebody a
# day: two peers need at least one of them reachable, and a laptop with a
# default-deny firewall cannot be reached even by a phone on the same wifi. Both
# ends then sit there announcing addresses, visible to each other over the
# rendezvous plane, with every handshake failing — which looks like anything
# except a closed port. A relay only gets an extra line at the bottom.
#
# Chosen by what is actually running rather than by distro, because the two
# disagree often enough to matter: Fedora ships firewalld but a server may run
# plain nftables, and Ubuntu ships ufw but frequently has it switched off. A
# command for the wrong tool is worse than none — it appears to work, changes
# nothing the active firewall consults, and the symptom stays.
#
# Two rules, not one, and they fail differently. The UDP port is the tunnel
# itself: without it this node cannot be dialled, which looks like a peer that
# never comes up. The interface rule is about traffic that has already arrived
# through the tunnel — a host firewall does not know the mesh interface is the
# mesh and files it under whatever it does with strangers, so `ssh host.mesh`
# works (ssh is usually allowed) while a service published on port 80 is
# refused. That pair of symptoms is a genuinely confusing thing to debug.
firewall_hint() {
    local port iface conf=/etc/shrooms/config.toml
    port=$(sed -n 's/^ *listen_port *= *\([0-9]\+\).*/\1/p' "$conf" 2>/dev/null | head -1)
    iface=$(sed -n 's/^ *interface *= *"\([^"]*\)".*/\1/p' "$conf" 2>/dev/null | head -1)
    port=${port:-51820}
    iface=${iface:-shrooms0}

    echo "==> firewall"

    if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
        cat <<EOF
firewalld is running. The mesh interface lands in the "public" zone, which
allows ssh and little else.

  sudo firewall-cmd --permanent --add-port=$port/udp
  sudo firewall-cmd --permanent --zone=trusted --add-interface=$iface
  sudo firewall-cmd --reload

The second line says traffic arriving over the mesh is trusted, which matches
how access is decided here: by membership, enforced by WireGuard, not by port.
On a mesh you share with other people do not do that — it gives their devices
everything on this machine, not only what you published. There, open the
specific ports instead:

  sudo firewall-cmd --permanent --add-port=80/tcp
EOF
    elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi '^Status: active'; then
        cat <<EOF
ufw is active.

  sudo ufw allow $port/udp
  sudo ufw allow in on $iface

The second line trusts anything arriving over the mesh, which is right for your
own devices. On a mesh shared with other people, allow the published ports
instead: sudo ufw allow in on $iface to any port 80 proto tcp
EOF
    elif command -v nft >/dev/null 2>&1 && nft list ruleset 2>/dev/null | grep -q 'chain input'; then
        cat <<EOF
nftables has rules loaded. Chain names vary, so check yours against
\`sudo nft list ruleset\` before pasting — these assume the common inet filter:

  sudo nft add rule inet filter input udp dport $port accept
  sudo nft add rule inet filter input iifname "$iface" accept

Added this way they are gone at reboot. Put them in /etc/nftables.conf, or
wherever your distribution keeps the ruleset it restores.
EOF
    elif command -v iptables >/dev/null 2>&1 && iptables -S 2>/dev/null | grep -q '^-A INPUT'; then
        cat <<EOF
iptables has rules in INPUT.

  sudo iptables -I INPUT -p udp --dport $port -j ACCEPT
  sudo iptables -I INPUT -i $iface -j ACCEPT

Those are lost at reboot unless something saves them — iptables-persistent on
Debian and Ubuntu, iptables-services on RHEL.
EOF
    else
        cat <<EOF
No active host firewall found, so there is probably nothing to open here. If a
peer still cannot reach this node, the block is upstream: a home router, or a
cloud provider's security group. Both need $port/udp forwarded to this machine.
EOF
    fi

    if [ "$RELAY" = yes ]; then
        cat <<EOF

This node relays for others, so being reachable is not optional for it: a relay
nobody can dial is a relay that does nothing.
EOF
    fi

    cat <<EOF

Each additional mesh uses the next port and interface up — a second mesh is
$((port + 1))/udp on ${iface}1.
EOF
}

# Read on the host, not by starting a container to grep a file. The copy this
# was ported from ran the image with --entrypoint /bin/sh for this one line,
# which is a container start, another SELinux relabel, and a different source of
# truth from the sed two lines above in firewall_hint.
RELAY=no
grep -q '^relay *= *"true"' /etc/shrooms/config.toml 2>/dev/null && RELAY=yes

firewall_hint
