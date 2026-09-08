#!/usr/bin/env bash
# Remove shrooms from THIS MACHINE, whichever way it was installed.
#
#   sudo ./scripts/uninstall.sh             # the software; config and identity stay
#   sudo ./scripts/uninstall.sh --purge     # everything, back to a bare machine
#
# For a node deployed with deploy.sh, which has no checkout to run this from:
#
#   ssh user@vps 'sudo bash -s -- --purge --yes' < scripts/uninstall.sh
#
# There are three installers and they put things in different places:
#
#   make install                 /usr/local/bin, /usr/local/lib/shrooms, a unit
#   scripts/install.sh           /etc/shrooms, a container, a host wrapper, a unit
#   packaging/install-dist.sh    /opt/shrooms, a symlink, a unit
#
# This removes what any of them created, so you do not have to remember which
# one you used. It also removes what none of them own but a *running* node
# leaves behind — the managed /etc/hosts block, a TUN link a crashed daemon did
# not take down, and resolved's registration for it.
#
# The pre-rename names (logos-vpn) are handled alongside the current ones. That
# matters more than it sounds: /var/lib/logos-vpn is still honoured when the new
# path is absent, so a machine that looked freshly installed would quietly come
# back with its old identity.
#
# What --purge adds, and why it is not the default:
#
#   /etc/shrooms      the config, including the network key
#   /var/lib/shrooms  the device identity and the mesh credential
#   the image         so the next install pulls rather than reusing a cached one
#
# What even --purge keeps: the mesh authority, wherever it is — an admin
# directory inside the config (/etc/shrooms/admin, which the node compose file
# mounts for a mesh minted in a container) and ~/.config/shrooms, which
# scripts/install.sh creates and the CLI defaults to. An admin key set is fixed
# when a mesh is minted, so a lost one cannot be replaced and the mesh can never
# admit another device: it is the only thing here that a reinstall cannot
# regenerate. --admin-keys-too removes it, for decommissioning the mesh itself.
#
# Deleting the identity is not an inconvenience to undo later — the node comes
# back with a different overlay address and is a stranger to every peer, each of
# which has to learn it again. So --purge lists what it found on this machine and
# asks before doing any of it. Say it when a clean machine is the actual point:
# testing the install flow end to end, or handing the box on.
#
# What it deliberately does not touch:
#
#   the checkout        `make clean` is that
#   the firewall        the rules to undo are printed, not run: they were typed
#                       by hand from install.sh's advice, and something else on
#                       this machine may now depend on them
#   the Android app     a separate install, removed on the phone
#   any real interface  the only links deleted are tunnels this project could
#                       have created. A NIC, wifi, bridge, bond, vlan or veth
#                       cannot be deleted by this script whatever it is named,
#                       and whatever the config says its interface is
set -euo pipefail

PURGE=0
ASSUME_YES=0
ADMIN_TOO=0

# Overridable so this can undo a `make install` that was given the same
# variables. The defaults match the Makefile's.
PREFIX=${PREFIX:-/usr/local}
BINDIR=${BINDIR:-$PREFIX/bin}
LIBDIR=${LIBDIR:-$PREFIX/lib/shrooms}
OPTDIR=${OPTDIR:-/opt/shrooms}
DESTDIR=${DESTDIR:-}

# Overridable for the same reason internal/hosts takes the path as an argument:
# the block-stripping edit is the one step here that can break name resolution
# for the whole machine, so it has to be exercisable against a copy.
HOSTS_FILE=${HOSTS_FILE:-/etc/hosts}

usage() {
    cat <<EOF
usage: sudo $0 [--purge] [--yes] [--admin-keys-too]

  --purge     also remove the config and the device identity
              (/etc/shrooms, /var/lib/shrooms and the pre-rename paths) and the
              container image. This is the "as if never installed" option; it
              lists what it found and asks before removing any of it.
  --yes, -y   do not ask. Required when stdin is not a terminal, which is the
              case when this script is itself piped in over ssh.

  --admin-keys-too
              with --purge, also remove the mesh authority: an admin directory
              inside the config (/etc/shrooms/admin) and ~/.config/shrooms.
              Kept by default even under --yes, because a lost admin key cannot
              be replaced and the mesh can then never admit another device.
              Say this only when decommissioning the mesh itself.

Without --purge the software goes and the mesh membership stays, so the node
comes back as the same device when you reinstall.
EOF
    exit 1
}

while [ $# -gt 0 ]; do
    case "$1" in
        --purge) PURGE=1; shift ;;
        --yes|-y) ASSUME_YES=1; shift ;;
        --admin-keys-too) ADMIN_TOO=1; shift ;;
        -h|--help) usage ;;
        *) echo "unknown option $1"; usage ;;
    esac
done

# Refused rather than ignored: somebody typing this means to lose the admin key,
# and a run that quietly kept it would be read as having removed it.
if [ $ADMIN_TOO -eq 1 ] && [ $PURGE -eq 0 ]; then
    echo "--admin-keys-too only means something with --purge"
    exit 1
fi

# DESTDIR is a staging directory — `make install DESTDIR=...` builds a package
# payload rather than installing. Nothing runs from there, so in that mode the
# file removals happen and systemd, the container runtime, /etc/hosts and the
# network are left alone.
STAGED=0
if [ -n "$DESTDIR" ]; then
    STAGED=1
fi

# Root is needed to change this machine, but a DESTDIR run only touches a
# directory the caller already owns — which is also what makes this script
# testable without sudo.
if [ "$(id -u)" -ne 0 ] && [ $STAGED -eq 0 ]; then
    echo "run as root (sudo $0 ...)"
    exit 1
fi

CONF=$DESTDIR/etc/shrooms/config.toml
if [ ! -f "$CONF" ]; then
    CONF=$DESTDIR/etc/logos-vpn/config.toml
fi

REMOVED=0
KEPT=""

say()  { printf '%s\n' "$*"; }
step() { printf '\n==> %s\n' "$*"; }

# Every command run here is cleanup, so every one of them wants its own output
# discarded and its failure tolerated: a `docker rm` that fails because the
# container is already gone is not news, and the caller says what happened.
run() { "$@" >/dev/null 2>&1 || true; }

# Whether a link is a tunnel, and therefore something this project could have
# created. The last word on every deletion, because a name is a guess and this
# is a fact about the device.
#
# `ip -d link show` names the kind. A userspace WireGuard TUN — which is what
# the daemon makes — is `link/none` with `tun type: tun`; a kernel WireGuard
# link is `link/none` with `wireguard`. Everything that carries a machine's real
# networking is `link/ether` or `link/loopback`: physical NICs, wifi, bridges,
# bonds, vlans, veths, dummies. None of those can pass this, so no name
# collision and no mistyped config can cost a machine its network.
#
# Both halves are required. `link/none` alone would admit other virtual link
# types, and a kind match alone would trust a string in the middle of a line.
#
# An `ip` too old or too cut-down to understand -d answers nothing, which reads
# as "not a tunnel" and deletes nothing. That is the right way for this to fail.
is_tunnel() {
    local d
    d=$(ip -d -o link show dev "$1" 2>/dev/null || true)
    case "$d" in
        *link/none*) ;;
        *) return 1 ;;
    esac
    case "$d" in
        *"tun type:"*|*wireguard*) return 0 ;;
    esac
    return 1
}

# Removal reports what was actually there. Listing everything this script knows
# about would bury the two or three lines that matter under a wall of paths that
# were never on this machine.
zap() {
    local p=$1
    if [ ! -e "$p" ] && [ ! -L "$p" ]; then
        return 0
    fi
    rm -rf "$p"
    say "    removed $p"
    REMOVED=$((REMOVED + 1))
}

# ---------------------------------------------------------------------------
# Discovery, all of it up front. Two reasons to separate it from the removal:
# the --purge prompt has to describe this machine before anything changes, and
# the config has to be read before something deletes it — the interface names
# decide which links to take down, and the port goes into the firewall advice.
# ---------------------------------------------------------------------------

PORT=$(sed -n 's/^ *listen_port *= *\([0-9]\+\).*/\1/p' "$CONF" 2>/dev/null | head -1 || true)
PORT=${PORT:-51820}
NAME=$(sed -n 's/^ *name *= *"\([^"]*\)".*/\1/p' "$CONF" 2>/dev/null | head -1 || true)

# Every interface the config names, top-level and per-mesh (ADR-015), so a mesh
# whose interface was renamed is still cleaned up.
CONF_IFACES=$(sed -n 's/^ *\(mesh\.[^.]*\.\)\?interface *= *"\([^"]*\)".*/\2/p' "$CONF" 2>/dev/null | sort -u || true)

# /run/systemd/system is the documented test for "booted with systemd", and the
# reason to use it rather than the presence of systemctl: a container often
# ships a stub systemctl that prints "systemd is not running" and still exits 0,
# so trusting an exit code here produces a script that claims to have stopped a
# service that was never there.
HAVE_SYSTEMD=0
if [ $STAGED -eq 0 ] && [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
    HAVE_SYSTEMD=1
fi

# Both names throughout, and both runtimes: install.sh uses whichever of docker
# and podman is present, and a machine can have been through the rename.
UNITS=""
if [ $HAVE_SYSTEMD -eq 1 ]; then
    for unit in shrooms.service logos-vpn.service; do
        # `systemctl cat`, not list-unit-files: the latter exits 0 with "0 unit
        # files listed" on some versions, which is not an answer.
        if systemctl cat "$unit" >/dev/null 2>&1; then
            UNITS="$UNITS $unit"
        fi
    done
fi

# The mesh authority, which is the one thing here that cannot be replaced. Two
# places hold it, and --purge would otherwise take both without saying so:
#
#   /etc/shrooms/admin       where a node that mints a mesh in a container is
#                            told to keep it (`admin init --dir`), and what
#                            docker/compose-node.yml mounts for exactly that
#   ~/.config/shrooms        the default, and a directory scripts/install.sh
#                            creates itself to mount into the container
#
# Losing it is not a setback. The admin key set is fixed when a mesh is minted —
# the mesh id is its hash — so a lost key cannot be replaced, the mesh can never
# admit another device, and members drop off as their credentials expire. See
# docs/when-a-node-loses-its-state.md, "The one that has no recovery".
ADMIN_IN_CONF=""
for p in "$DESTDIR/etc/shrooms/admin" "$DESTDIR/etc/shrooms"/admin*.json; do
    if [ -e "$p" ]; then
        ADMIN_IN_CONF="$ADMIN_IN_CONF $p"
    fi
done

# Resolved the way the CLI and install.sh both resolve it: under sudo the home
# that matters is the invoking user's, because an admin key belongs to the
# person rather than to the machine.
ADMIN_HOME=$(getent passwd "${SUDO_USER:-$(id -un)}" 2>/dev/null | cut -d: -f6 || true)
ADMIN_DIR=${ADMIN_HOME:-$HOME}/.config/shrooms
if [ -n "$DESTDIR" ] || [ ! -d "$ADMIN_DIR" ]; then
    ADMIN_DIR=""
fi

RUNTIME=$(command -v docker || command -v podman || true)
CONTAINERS=""
IMAGES=""
if [ $STAGED -eq 0 ] && [ -n "$RUNTIME" ]; then
    for c in shrooms logos-vpn; do
        # `container inspect` rather than plain `inspect`, which also matches
        # images and would report a container that does not exist.
        if "$RUNTIME" container inspect "$c" >/dev/null 2>&1; then
            CONTAINERS="$CONTAINERS $c"
        fi
    done
    if [ $PURGE -eq 1 ]; then
        IMAGES=$("$RUNTIME" images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null \
                 | grep -E '/(shrooms|logos-vpn):' || true)
    fi
fi

say "shrooms uninstall${NAME:+, on \"$NAME\"}"
if [ -f "$CONF" ]; then
    say "  config found at $CONF"
fi

# ---------------------------------------------------------------------------
# Confirm the destructive half — once, up front, and against what is actually
# here rather than against the list of everything this script knows how to
# remove. The question is "do you mean to lose the identity", and it has one
# answer, so it is asked once and not per path.
# ---------------------------------------------------------------------------

# What --purge would take, annotated. Built as a list so the prompt can be
# skipped entirely when there is nothing on this machine to lose.
purge_inventory() {
    local p conf="config, including the network key"
    # Saying "/etc/shrooms" while keeping a directory inside it would contradict
    # the "leave alone" list printed below it.
    if [ -n "$ADMIN_IN_CONF" ] && [ $ADMIN_TOO -eq 0 ]; then
        conf="config — all of it but the admin directory"
    fi
    for p in "$DESTDIR/etc/shrooms:$conf" \
             "$DESTDIR/var/lib/shrooms:device identity${NAME:+ (\"$NAME\")}" \
             "$DESTDIR/run/shrooms:control socket" \
             "$DESTDIR/etc/logos-vpn:config, from before the rename" \
             "$DESTDIR/var/lib/logos-vpn:device identity, from before the rename"; do
        if [ -e "${p%%:*}" ]; then
            printf '  %-34s %s\n' "${p%%:*}" "${p#*:}"
        fi
    done
    for p in $IMAGES; do
        printf '  %-34s %s\n' "$p" "image, so the next install pulls one"
    done
    if [ $ADMIN_TOO -eq 1 ]; then
        for p in $ADMIN_IN_CONF $ADMIN_DIR; do
            printf '  %-34s %s\n' "$p" "THE MESH AUTHORITY — not replaceable"
        done
    fi
}

# The counterpart: what --purge finds and deliberately does not take. Printed
# beside the removal list rather than only in the report at the end, because
# "it kept my admin key" is something to know before typing yes, not after.
purge_kept() {
    local p
    if [ $ADMIN_TOO -eq 1 ]; then
        return 0
    fi
    for p in $ADMIN_IN_CONF $ADMIN_DIR; do
        printf '  %-34s %s\n' "$p" "the mesh authority"
    done
}

if [ $PURGE -eq 1 ] && [ $ASSUME_YES -eq 0 ]; then
    inventory=$(purge_inventory)
    if [ -n "$inventory" ]; then
        if [ ! -t 0 ]; then
            # Being piped in over ssh is the normal way to run this on a remote
            # node, and then stdin is the script itself: `read` would answer the
            # prompt with a line of bash.
            echo "--purge needs --yes when stdin is not a terminal"
            exit 1
        fi
        say ""
        say "--purge will remove, on this machine:"
        say ""
        say "$inventory"
        # "the shrooms.service unit and the shrooms container", assembled rather
        # than hardcoded: on a machine that has been through the rename there
        # can be two of each, and naming the ones actually found is the point.
        stops=""
        for unit in $UNITS; do
            stops="${stops:+$stops and }the $unit unit"
        done
        for c in $CONTAINERS; do
            stops="${stops:+$stops and }the $c container"
        done
        if [ -n "$stops" ]; then
            say ""
            say "and stop $stops."
        fi

        kept=$(purge_kept)
        if [ -n "$kept" ]; then
            say ""
            say "and leave alone, because losing it cannot be undone:"
            say ""
            say "$kept"
        fi

        cat <<EOF

This machine rejoins as a NEW device with a different overlay address, and every
peer has to learn it. Membership comes from a credential an admin signs, so
getting back in means a new invite rather than a copy of the config.

EOF
        if [ $ADMIN_TOO -eq 1 ] && [ -n "$ADMIN_IN_CONF$ADMIN_DIR" ]; then
            cat <<EOF
--admin-keys-too was given, so the mesh authority above goes with it. That ends
the mesh: its admin keys are fixed at mint, so nothing can issue a credential or
revoke one again, and members drop off as theirs expire.

EOF
        fi
        printf 'type "yes" to continue: '
        read -r answer
        if [ "$answer" != yes ]; then
            echo "nothing was changed"
            exit 1
        fi
    fi
fi

# ---------------------------------------------------------------------------
# Stop it first, so the daemon is not writing to paths as they go — and so a
# container is stopped rather than left running against a deleted image.
# ---------------------------------------------------------------------------

if [ -n "$UNITS" ]; then
    step "stopping and disabling the service"
    for unit in $UNITS; do
        run systemctl disable --now "$unit"
        say "    stopped $unit"
    done
fi

for c in $CONTAINERS; do
    step "removing the $c container"
    run "$RUNTIME" rm -f "$c"
    say "    removed container $c"
done

# A daemon started by hand from a checkout is under neither systemd nor a
# container, so nothing above stopped it. Deleting its interface underneath it
# would leave a node that thinks it is up and a machine that disagrees, so it is
# reported rather than guessed at.
#
# Only looked for when nothing managed was found, because a containerised daemon
# is an ordinary host process too, and pgrep cannot tell the two apart.
STRAY=""
if [ $STAGED -eq 0 ] && [ -z "$UNITS$CONTAINERS" ] && command -v pgrep >/dev/null 2>&1; then
    STRAY=$(pgrep -f '[s]hrooms daemon' 2>/dev/null | tr '\n' ' ' || true)
fi

# ---------------------------------------------------------------------------
# Files, from every installer.
# ---------------------------------------------------------------------------

step "removing the software"

# The host-facing `shrooms`: a real binary from `make install`, a wrapper script
# from install.sh, or a symlink into /opt from the portable installer. All three
# live at the same path, and all three simply go.
zap "$DESTDIR$BINDIR/shrooms"
zap "$DESTDIR$BINDIR/logos-vpn"
zap "$DESTDIR$LIBDIR"
zap "$DESTDIR$OPTDIR"

for u in shrooms.service logos-vpn.service; do
    zap "$DESTDIR/etc/systemd/system/$u"
    zap "$DESTDIR/usr/lib/systemd/system/$u"
done

# Completion lands in whichever of these the installer found, so all are checked.
for d in /usr/share/bash-completion/completions \
         /usr/local/share/bash-completion/completions \
         "$PREFIX/share/bash-completion/completions"; do
    zap "$DESTDIR$d/shrooms"
done

if [ $REMOVED -gt 0 ] && [ $HAVE_SYSTEMD -eq 1 ]; then
    run systemctl daemon-reload
fi

# ---------------------------------------------------------------------------
# What the running daemon left. No installer created these, so no installer
# removed them.
# ---------------------------------------------------------------------------

if [ $STAGED -eq 0 ]; then
    step "removing what the daemon left behind"

    # /etc/hosts. Stale mesh names that still resolve are worse than names that
    # do not: a connection attempt goes to an address nothing answers on.
    hosts=$HOSTS_FILE
    if ! grep -q '^# BEGIN logos-vpn' "$hosts" 2>/dev/null; then
        :
    elif ! grep -q '^# END logos-vpn' "$hosts" 2>/dev/null; then
        # A begin marker with no end is a file somebody edited inside the block.
        # The awk below would treat everything after it as part of the block and
        # delete the rest of /etc/hosts, so this stops instead.
        say "    left $hosts alone: the managed block has no end marker"
        say "      remove the '# BEGIN logos-vpn' block by hand"
    else
        # Rewritten via a temporary file in the same directory and moved into
        # place, the way internal/hosts does it: a torn or emptied /etc/hosts
        # breaks name resolution for the whole machine, including whatever you
        # would use to fix it. Hence both guards below — a failed awk can leave
        # partial output, which is the same accident wearing a non-empty file.
        tmp=$(mktemp "$(dirname "$hosts")/.shrooms-hosts.XXXXXX")
        if ! awk '/^# BEGIN logos-vpn/{skip=1} !skip{print} /^# END logos-vpn/{skip=0}' \
                "$hosts" >"$tmp"; then
            rm -f "$tmp"
            say "    left $hosts alone: could not rewrite it cleanly"
            say "      remove the '# BEGIN logos-vpn' block by hand"
        elif [ ! -s "$tmp" ]; then
            rm -f "$tmp"
            say "    left $hosts alone: stripping the block would empty it"
        else
            # Mode and owner from the file being replaced, not assumed: a
            # /etc/hosts that comes back root-owned on a system where it was not
            # is a difference nobody would think to look for.
            chmod --reference="$hosts" "$tmp" 2>/dev/null || chmod 0644 "$tmp"
            chown --reference="$hosts" "$tmp" 2>/dev/null || true
            mv "$tmp" "$hosts"
            say "    removed the managed block from $hosts"
            REMOVED=$((REMOVED + 1))
        fi
    fi

    # Interfaces. The daemon takes its own TUN down when it exits, and it was
    # stopped above, so anything still here outlived a crash or a kill -9.
    # Matched by the naming scheme (shrooms0, shrooms01, ... — one per mesh)
    # plus whatever the config named.
    #
    # A name is necessary here but never sufficient: nothing is deleted unless
    # it is also a tunnel (see is_tunnel). Deleting a link is the one thing in
    # this script that can take a machine off the network entirely, and it would
    # do it from a name in a config file that the daemon does not have to have
    # created. That is too much trust for a string.
    if [ -n "$STRAY" ]; then
        say "    left the interfaces alone: a daemon is still running (pid $STRAY)"
        say "      deleting its tunnel would leave it believing it is up"
    elif command -v ip >/dev/null 2>&1; then
        live=$(ip -o link show 2>/dev/null | awk -F': ' '{print $2}' | sed 's/@.*//' || true)
        for iface in $live; do
            named=0
            case "$iface" in
                shrooms[0-9]*) named=1 ;;
            esac
            for c in $CONF_IFACES; do
                if [ "$iface" = "$c" ]; then
                    named=1
                fi
            done
            if [ $named -eq 0 ]; then
                continue
            fi

            # A real interface wearing a matching name — most plausibly a config
            # whose `interface` was pointed at a NIC by mistake, since the daemon
            # would then have failed to create its tunnel at all. Reported, not
            # deleted, and not silently skipped either: the name matched, so
            # saying nothing would look like the sweep had not run.
            if ! is_tunnel "$iface"; then
                say "    left $iface alone: it matches the naming scheme but is"
                say "      not a tunnel, so it is not ours to delete"
                continue
            fi

            # resolved forgets a link when it disappears, but reverting first
            # means a search domain cannot outlive the interface it came from.
            if command -v resolvectl >/dev/null 2>&1; then
                run resolvectl revert "$iface"
            fi
            run ip link delete "$iface"
            say "    deleted stranded interface $iface"
            REMOVED=$((REMOVED + 1))
        done
    fi
fi

# ---------------------------------------------------------------------------
# The purge: config, identity, and the image.
# ---------------------------------------------------------------------------

if [ $PURGE -eq 1 ]; then
    step "purging config and identity"

    # An admin key inside the config directory is stepped around rather than
    # taken with it. The check lives here and not only in the prompt because
    # --yes skips the prompt entirely — and --yes is the normal case, being what
    # an unattended run and the piped-over-ssh form both need. A silent
    # `rm -rf /etc/shrooms` there is precisely the accident that
    # docs/when-a-node-loses-its-state.md calls the one with no recovery.
    if [ -n "$ADMIN_IN_CONF" ] && [ $ADMIN_TOO -eq 0 ]; then
        for entry in "$DESTDIR/etc/shrooms"/* "$DESTDIR/etc/shrooms"/.*; do
            case "${entry##*/}" in
                '*'|.|..|'.*') continue ;;
                admin|admin*.json) continue ;;
            esac
            zap "$entry"
        done
        say "    kept the mesh authority:$ADMIN_IN_CONF"
        say "      it cannot be replaced, and this mesh could never admit"
        say "      another device without it (--admin-keys-too to remove)"
    else
        zap "$DESTDIR/etc/shrooms"
    fi

    zap "$DESTDIR/var/lib/shrooms"
    zap "$DESTDIR/run/shrooms"
    zap "$DESTDIR/etc/logos-vpn"
    zap "$DESTDIR/var/lib/logos-vpn"

    # ~/.config/shrooms, which scripts/install.sh creates and the CLI defaults
    # to. Only ever on request: it holds the same authority, and it belongs to
    # the person rather than to this machine.
    if [ -n "$ADMIN_DIR" ] && [ $ADMIN_TOO -eq 1 ]; then
        zap "$ADMIN_DIR"
    fi

    # The image, so the next install pulls one rather than reusing what is
    # cached and calling that a fresh machine. Every tag of ours, since :latest
    # is only the usual one.
    if [ -n "$IMAGES" ]; then
        step "removing the container image"
        for img in $IMAGES; do
            run "$RUNTIME" rmi "$img"
            say "    removed image $img"
            REMOVED=$((REMOVED + 1))
        done
    fi
else
    for p in /etc/shrooms /var/lib/shrooms /etc/logos-vpn /var/lib/logos-vpn; do
        if [ -e "$DESTDIR$p" ]; then
            KEPT="$KEPT$DESTDIR$p"$'\n'
        fi
    done
fi

# ---------------------------------------------------------------------------
# Report. What went, what stayed, and what this could not do for you.
# ---------------------------------------------------------------------------

step "done"

if [ $REMOVED -eq 0 ]; then
    if [ $PURGE -eq 1 ]; then
        say "nothing to remove — this machine has no shrooms install and no config"
    else
        say "nothing to remove — this machine has no shrooms install"
    fi
fi

if [ -n "$KEPT" ]; then
    say ""
    say "kept, so this machine rejoins as the same device:"
    printf '  %s\n' $KEPT
    say ""
    say "for a machine that has never seen shrooms — testing the install flow —"
    say "run this again with --purge."
fi

if [ -n "$STRAY" ]; then
    say ""
    say "a shrooms daemon is still running outside systemd (pid $STRAY)."
    say "it was started by hand, so nothing here stopped it:"
    say "  sudo kill $STRAY"
fi

# The admin directory, always reported and never removed unless asked for. It is
# the one thing here a reinstall cannot regenerate, and scripts/install.sh
# creates it — so leaving it out of the report would make this script's claim to
# undo that installer untrue.
if [ -n "$ADMIN_DIR" ] && [ $ADMIN_TOO -eq 0 ]; then
    say ""
    say "left alone, in your home rather than on this machine:"
    say "  $ADMIN_DIR"
    say "    admin keys for any mesh minted from here, and the keycard pairing."
    say "    Losing them means the mesh can never admit another device, so they"
    say "    outlive the install on purpose. --purge --admin-keys-too takes them."
fi

# Firewall advice, in the same shape install.sh gives it and for the same
# reason: chosen by what is actually running, because a command for the wrong
# tool appears to work and changes nothing.
say ""
say "the firewall was not touched. If you opened anything for shrooms:"
iface=$(printf '%s\n' $CONF_IFACES | head -1)
iface=${iface:-shrooms0}
if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    say "  sudo firewall-cmd --permanent --remove-port=$PORT/udp"
    say "  sudo firewall-cmd --permanent --zone=trusted --remove-interface=$iface"
    say "  sudo firewall-cmd --reload"
elif command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi '^Status: active'; then
    say "  sudo ufw delete allow $PORT/udp"
    say "  sudo ufw delete allow in on $iface"
elif command -v nft >/dev/null 2>&1 && nft list ruleset 2>/dev/null | grep -q 'chain input'; then
    say "  sudo nft -a list ruleset          # find the handles for $PORT/udp and $iface"
    say "  sudo nft delete rule inet filter input handle <N>"
    say "  and drop them from wherever the ruleset is restored at boot"
elif command -v iptables >/dev/null 2>&1 && iptables -S 2>/dev/null | grep -q '^-A INPUT'; then
    say "  sudo iptables -D INPUT -p udp --dport $PORT -j ACCEPT"
    say "  sudo iptables -D INPUT -i $iface -j ACCEPT"
    say "  then save them, or they come back at boot"
else
    say "  no active host firewall found here, so there is probably nothing to undo"
fi
say "  a router or cloud security group forwarding $PORT/udp is separate again"

say ""
say "peers drop this device from their roster about three minutes after it stops"
say "answering; nothing has to be done on them."
