#!/usr/bin/env sh
# Install a portable shrooms build on this machine.
#
#   sudo ./install.sh
#
# For a machine with neither a Go toolchain nor a container runtime, which is
# most machines. Everything here is already built.
#
# Installed as a self-contained tree under /opt/shrooms with a symlink on the
# path, rather than scattered into /usr/local. Two reasons: the binary finds its
# libraries by a RUNPATH of $ORIGIN/../lib, which only holds if they stay beside
# it; and those libraries include libssl and libpq, which have no business
# landing in a directory the system linker searches for everything else.
#
# What it touches:
#   /opt/shrooms/                       binary and libraries
#   /usr/local/bin/shrooms              symlink
#   /etc/systemd/system/shrooms.service
#   bash completion, if the directory exists
#
# It does not create a config or start anything. On a fresh machine the daemon
# comes up waiting for a mesh, and `shrooms join --invite <TOKEN>` finishes it.
#
# uninstall.sh beside this script undoes it, and knows about /opt/shrooms as
# well as the paths the other two installers use.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=${ROOT:-/opt/shrooms}
bindir=${BINDIR:-/usr/local/bin}

[ "$(id -u)" = 0 ] || { echo "run this with sudo"; exit 1; }
[ -f "$here/bin/shrooms" ] || { echo "no bin/shrooms beside this script"; exit 1; }

echo "==> installing to $root"
install -d "$root/bin" "$root/lib"
install -m 0644 "$here"/lib/*.so* "$root/lib/"
install -m 0755 "$here/bin/shrooms" "$root/bin/shrooms"

install -d "$bindir"
ln -sf "$root/bin/shrooms" "$bindir/shrooms"

if [ -f "$here/shrooms.service" ]; then
    install -d /etc/systemd/system
    # The unit ships pointing at the source layout's library directory. Rewritten
    # rather than shipped twice, so there is one unit file to keep correct.
    sed -e "s|^Environment=LD_LIBRARY_PATH=.*|Environment=LD_LIBRARY_PATH=$root/lib|" \
        -e "s|^ExecStart=.*|ExecStart=$root/bin/shrooms daemon|" \
        "$here/shrooms.service" > /etc/systemd/system/shrooms.service
    chmod 0644 /etc/systemd/system/shrooms.service
fi

for d in /usr/share/bash-completion/completions /usr/local/share/bash-completion/completions; do
    if [ -f "$here/shrooms.bash" ] && [ -d "$d" ]; then
        install -m 0644 "$here/shrooms.bash" "$d/shrooms"
        break
    fi
done

# SELinux, on Fedora and RHEL: a file written into /opt keeps the context of
# wherever it was copied from, and systemd will refuse to execute it. restorecon
# gives it the type the policy expects. Absent elsewhere, hence the guard.
if command -v restorecon >/dev/null 2>&1; then
    restorecon -R "$root" 2>/dev/null || true
fi

# Checked here rather than discovered later: a binary that cannot find its
# libraries fails on first run with a linker error nobody enjoys reading.
if ! "$root/bin/shrooms" version >/dev/null 2>&1; then
    echo "installed, but the binary does not run. Check:"
    echo "  ldd $root/bin/shrooms"
    exit 1
fi

echo "    $("$root/bin/shrooms" version)"
cat <<NEXT

Next, on this machine:
  sudo systemctl daemon-reload
  sudo systemctl enable --now shrooms      # comes up waiting for a mesh

Then, on a machine already on the mesh:
  shrooms invite

and back here:
  sudo shrooms join --invite <TOKEN> --name \$(hostname)

If this machine is publicly reachable, open its port (51820, plus one per
extra mesh):
  sudo firewall-cmd --add-port=51820/udp --permanent && sudo firewall-cmd --reload
  # or: sudo ufw allow 51820/udp

To undo this, from the same directory:
  sudo ./uninstall.sh            # the software
  sudo ./uninstall.sh --purge    # config and identity too
NEXT
