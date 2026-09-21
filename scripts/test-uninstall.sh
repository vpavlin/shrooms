#!/usr/bin/env bash
# Exercise scripts/uninstall.sh against a staged tree, as a test.
#
# This exists because the one bug a reviewer found in that script was the kind
# only a real run finds: `is_tunnel` matched "tun type:" with a colon, which
# iproute2 has never printed, so the stranded-interface sweep silently deleted
# nothing while the docs said it did. Reading the script did not catch it and
# neither did a careful author.
#
# Nothing here touches this machine. DESTDIR stages every path, HOSTS_FILE
# points at a copy, and no step needs root — which is what lets CI run it.
#
#   ./scripts/test-uninstall.sh
set -euo pipefail

UNINSTALL=$(cd "$(dirname "$0")" && pwd)/uninstall.sh
PASS=0
FAIL=0

ok()   { PASS=$((PASS + 1)); printf '  ok    %s\n' "$1"; }
bad()  { FAIL=$((FAIL + 1)); printf '  FAIL  %s\n' "$1"; }
gone() { [ -e "$1" ] && bad "$2 (still there: $1)" || ok "$2"; }
kept() { [ -e "$1" ] && ok "$2" || bad "$2 (missing: $1)"; }

# A machine that has been installed every way there is: `make install`, the
# container installer, and the portable tarball.
stage() {
    local root=$1
    mkdir -p "$root"/usr/local/bin "$root"/usr/local/lib/shrooms \
             "$root"/etc/systemd/system "$root"/etc/shrooms "$root"/var/lib/shrooms \
             "$root"/opt/shrooms/bin "$root"/usr/share/bash-completion/completions \
             "$root"/etc/logos-vpn "$root"/var/lib/logos-vpn

    : > "$root"/usr/local/bin/shrooms                       # make install, or the wrapper
    : > "$root"/usr/local/bin/logos-vpn                     # the pre-rename name
    : > "$root"/usr/local/lib/shrooms/liblogosdelivery.so
    : > "$root"/usr/local/lib/shrooms/register-dns          # the resolver registrar
    : > "$root"/opt/shrooms/bin/shrooms                     # install-dist.sh
    : > "$root"/usr/share/bash-completion/completions/shrooms
    for u in shrooms shrooms-resolved logos-vpn; do
        : > "$root/etc/systemd/system/$u.service"
    done

    cat > "$root"/etc/shrooms/config.toml <<'CFG'
name        = "nas"
interface   = "shrooms0"
network_key = "GUPDIZSVIQRCBBC6APQFYDSDKTWK6Q5SYZY6ZLMVECQDQX3GIOYQ"
CFG
    : > "$root"/var/lib/shrooms/state.json
    mkdir -p "$root"/etc/shrooms/admin
    : > "$root"/etc/shrooms/admin/admin.json                # the mesh authority
}

# A hosts file with a managed block in the middle of entries that matter.
stage_hosts() {
    cat > "$1" <<'HOSTS'
127.0.0.1	localhost
::1		localhost ip6-localhost ip6-loopback
192.168.0.10	printer.lan
# BEGIN logos-vpn — managed, do not edit inside this block
fd25:329f:a0d6:343:8fb6:5502:b802:42fd	laptop.mesh
198.18.142.149	nas.mesh
# END logos-vpn
10.0.0.5	buildserver
HOSTS
}

run_uninstall() {
    local root=$1 hosts=$2
    shift 2
    DESTDIR="$root" PREFIX=/usr/local HOSTS_FILE="$hosts" \
        bash "$UNINSTALL" "$@" > "$root/../out.log" 2>&1
}

echo "staged uninstall, --purge --yes"
T=$(mktemp -d)
R=$T/root
H=$T/hosts
stage "$R"
stage_hosts "$H"
run_uninstall "$R" "$H" --purge --yes

gone "$R/usr/local/bin/shrooms"                              "the binary or wrapper goes"
gone "$R/usr/local/bin/logos-vpn"                            "the pre-rename binary goes"
gone "$R/usr/local/lib/shrooms"                              "the library directory goes, registrar and all"
gone "$R/opt/shrooms"                                        "the portable install goes"
gone "$R/usr/share/bash-completion/completions/shrooms"       "completion goes"
gone "$R/etc/systemd/system/shrooms.service"                 "the unit goes"
gone "$R/etc/systemd/system/shrooms-resolved.service"        "the resolver unit goes"
gone "$R/etc/systemd/system/logos-vpn.service"               "the pre-rename unit goes"
gone "$R/var/lib/shrooms"                                    "--purge takes the identity"
gone "$R/etc/logos-vpn"                                      "--purge takes the pre-rename config"

# The one thing a reinstall cannot regenerate. --purge alone must never take it.
kept "$R/etc/shrooms/admin/admin.json"                       "the mesh authority survives --purge"

echo
echo "the /etc/hosts block"
if grep -q 'logos-vpn' "$H"; then
    bad "the managed block is stripped"
else
    ok "the managed block is stripped"
fi
for entry in localhost printer.lan buildserver ip6-loopback; do
    grep -q "$entry" "$H" && ok "$entry is untouched" || bad "$entry is untouched"
done
# Nothing of the mesh may answer afterwards.
grep -qE 'laptop\.mesh|nas\.mesh' "$H" && bad "no mesh name still resolves" || ok "no mesh name still resolves"

echo
echo "staged uninstall, no --purge"
T2=$(mktemp -d)
R2=$T2/root
H2=$T2/hosts
stage "$R2"
stage_hosts "$H2"
run_uninstall "$R2" "$H2" --yes

gone "$R2/usr/local/bin/shrooms"                             "the software still goes"
kept "$R2/etc/shrooms/config.toml"                           "the config stays, so the device rejoins as itself"
kept "$R2/var/lib/shrooms/state.json"                        "the identity stays"

echo
echo "staged uninstall, --purge --admin-keys-too"
T3=$(mktemp -d)
R3=$T3/root
H3=$T3/hosts
stage "$R3"
stage_hosts "$H3"
run_uninstall "$R3" "$H3" --purge --admin-keys-too --yes
gone "$R3/etc/shrooms/admin/admin.json"                      "--admin-keys-too takes the authority"

echo
echo "a hosts file somebody edited inside the block is left alone"
T4=$(mktemp -d)
R4=$T4/root
H4=$T4/hosts
stage "$R4"
printf '127.0.0.1\tlocalhost\n# BEGIN logos-vpn — managed, do not edit inside this block\nfd25::1\tlaptop.mesh\n' > "$H4"
before=$(cat "$H4")
run_uninstall "$R4" "$H4" --purge --yes
[ "$before" = "$(cat "$H4")" ] && ok "a block with no end marker is untouched" \
                               || bad "a block with no end marker is untouched"

rm -rf "$T" "$T2" "$T3" "$T4"

echo
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
