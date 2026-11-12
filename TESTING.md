# Test paths

Scenarios worth running deliberately, each with a setup, what to watch, and what
counts as a pass. They exist because most of this project's bugs were
timing- or topology-dependent and invisible in containers — a passing container
test told us nothing three separate times.

**Read the numbers, not the vibe.** Every node reports three timings separately
(discovered / path / tunnel) precisely so a failure names its layer.

```console
$ shrooms status          # who, how, how fast, and how long it took
$ shrooms paths           # which candidates answered, and which is in use
```

---

## Does M2 need re-verifying?

**Yes, and it is nearly done.** The phone relaying on 5G and going direct on
wifi is real evidence, but it is not yet a verdict, because two different things
produce it:

1. Punching was attempted and **failed** because the carrier's CGNAT is
   endpoint-dependent — the case the relay exists for. M2 answered, negatively,
   correctly.
2. Punching **never got a fair attempt** — the relay was selected first and the
   direct path was never established.

Those look identical from the outside and mean opposite things about the code.

**The distinguishing evidence is already collected.** `shrooms paths` gives a
verdict per mesh on this node's NAT, from where its peers say they see it, and
for each peer where that peer's probes arrive from:

```
NAT on home: a new port for every destination — direct only to a peer whose NAT is forgiving, otherwise the relay
  seen at 203.0.113.4:41001 by 1
  seen at 203.0.113.4:41002 by 1

vps  fd3b:…
  its probes arrive from 198.51.100.7:51820 (3s ago)
```

The three verdicts are `maps the same way for everyone` (endpoint-independent:
a direct path to another NATed peer can work), `a new port for every
destination` (endpoint-dependent: punching cannot work), and `not known yet`,
which needs at least two peers reporting. A relayed peer whose probes never
arrive says `none of its probes has arrived here`.

So the test is: on the phone, on mobile data, with at least two peers, read
`paths`. A new port per destination means no amount of correct punching would
help — the relay is the right answer and M2 is answered. The same mapping for
everyone on both ends, with the relay still in use, means punching should have
worked and did not, which is a bug.

Run **T3** below to settle it.

---

## T1 — two reachable nodes

The baseline. If this is slow or flaky, nothing further is worth diagnosing.

**Setup:** two hosts with public addresses, e.g. two VPSes.
**Watch:** `shrooms status` on both.
**Pass:** both `up`, endpoint is the peer's public address with no `relay:`
prefix, `CONNECTED IN` a few seconds.

---

## T2 — one NATed, one reachable

The common case: laptop to VPS.

**Setup:** a machine behind a home router, and a VPS.
**Pass:** direct in both directions. The NATed side is reached because it spoke
first, so its mapping is open — no relay should appear.

**If it relays here**, something is wrong: this case needs no punching at all.

---

## T3 — two NATed nodes, different networks *(the M2 verdict)*

The case containers cannot honestly reproduce, because a NAT built by a test
harness is a NAT built to be cooperative.

**Setup:** phone on mobile data, laptop on home wifi. Nothing else changes.
**Watch:** `shrooms paths` on both, and the endpoint in `status`.

**Pass — either outcome, provided it is the honest one:**

- Direct, and each node's `NAT on <mesh>:` line says it maps the same way for
  everyone. Punching works between these NATs.
- Relayed, and at least one node's `NAT on <mesh>:` line says a new port for
  every destination. Punching cannot work here and the relay correctly took
  over.

**Fail:** relayed while both nodes map the same way for everyone. That is
punching not being attempted properly, not a hostile NAT. Read each side's
`its probes arrive from` for the other: probes that never arrive say which
direction is blocked.

Repeat on a second carrier or a café network before concluding anything general;
one NAT is one data point.

---

## T4 — two nodes on the same LAN

Easy to forget, and the one where an overlay can embarrass itself by routing two
machines on the same switch through a VPS on another continent.

**Setup:** two machines on one network, plus a relay somewhere so the wrong
answer is available.
**Pass:** direct, via a private address, with sub-millisecond RTT in `paths`.

---

## T5 — relay failover and recovery

**Setup:** any two peers with a working direct path, plus a relay.
**Do:** block the direct path — on one host, `sudo iptables -A OUTPUT -p udp
--dport 51820 -d <peer public ip> -j DROP`.
**Pass:** traffic continues within ~30s, `status` shows a `relay:` endpoint.
**Then:** remove the rule. **Pass:** it returns to direct.

Watch that the return to direct does not thrash — path selection is deliberately
sticky (`SwitchMargin`), and a peer flipping every few seconds is a regression.

---

## T6 — roaming

The daily-driver test. **Verified once on Android; worth repeating deliberately.**

**Do:** with the tunnel up and traffic flowing (`ping6 <peer>.<mesh>.mesh`), turn wifi
off so the phone falls to mobile data. Then back on.
**Pass:** the overlay survives both transitions without user action. Expect the
path to change — direct on wifi, possibly relayed on mobile — and the ping to
resume within a few seconds.

**Watch for:** a tunnel that reports `up` while nothing flows. That is the
failure mode `status` was changed to make visible; the handshake age tells you.

---

## T7 — restart, and reconnect time

**Do:** restart one side. `sudo systemctl restart shrooms`, or force-stop the
app.
**Pass:** reconnects in seconds, and `tunnel established` reports it:

```
tunnel established peer=vps after=447ms discovered_after=278ms path_after=446ms
```

**Watch for:** minutes rather than seconds. That was a real bug (batch
compaction corrupting handshakes) and the three-stage timing is what exposed it —
discovery and path were fast while the tunnel took six minutes.

---

## T8 — rendezvous outage

Waku is rendezvous, not a control plane (DESIGN §2), and this is the test of
whether that is true in practice rather than only in the document.

**Do:** with tunnels established, cut one host off the rendezvous plane. The
fleet's port 30303 alone is not enough: a node also bootstraps from the
delivery port its members publish (ADR-031, `delivery_port`), so block that
too, or simply every outbound TCP connection that does not go over the mesh —
the tunnels are UDP:
`sudo iptables -A OUTPUT -p tcp ! -o shrooms0 -j DROP`.
**Pass:** existing tunnels keep carrying traffic. `status` shows the rendezvous
warning and says explicitly that tunnels are unaffected.
**Fail:** tunnels drop. That would mean something has quietly started depending
on the rendezvous plane.

**Then restore.** Discovery should resume without a restart, and without waiting
out a long backoff.

---

## T9 — names

**Do:** `ping6 <peer>.<mesh>.mesh`, then `curl https://github.com`.
**Pass:** the first resolves through the mesh resolver, the second through the
system's. `resolvectl query <peer>.<mesh>.mesh` should name `shrooms0` as the link.

**Watch for:** the second failing. A resolver that takes over everything and
answers only for the mesh removes the device's name resolution — that happened
three times on Android and presented as "DNS is broken" each time.

---

## T10 — a published service

The interesting case is deliberately the awkward one: an application that binds
`0.0.0.0` and nothing else, which is most of them.

**Setup:** on one machine, something listening on IPv4 loopback only — a real
application, or `python3 -m http.server 8000 --bind 127.0.0.1`. Then:

```console
$ sudo shrooms services add test --port 8000
$ sudo shrooms reload                           # applies services without a restart
```

**Watch:** `shrooms status` on that machine lists it under *services
published here*, with the name to type.

**Pass:** from another device, both of these return the page:

```console
$ curl http://test.<device>.<mesh>.mesh          # via the shared-port name router
$ curl http://test.<device>.<mesh>.mesh:8000     # the declared port
```

From the phone's browser too — same name, no port forwarding, nothing
configured on either end.

**Watch for:**

- *The port works and the bare name does not.* The name router did not get port
  80. `status` says why on a note under the table; the usual cause is a missing
  `CAP_NET_BIND_SERVICE`, the same capability the resolver needs for port 53.
- *Resolves but the connection is refused.* The name is fine and the forwarder
  is not running. Check `status` on the publishing machine: a service that
  failed to bind says so on its row.
- *Does not resolve at all.* The device half of the name is wrong, or the
  resolver is not registered — `resolvectl query test.<device>.<mesh>.mesh` names the
  link it asked.
- *A name for a service that does not exist still resolves.* Correct. Services
  are not announced, so the name resolves to the machine that would run it, and
  an HTTP request there gets a 404 listing the names that do exist.

**Also check a protocol that is not HTTP.** `ssh -p 22 ssh.<device>.<mesh>.mesh` with
`services = ["ssh:22"]` must work *with* the port and must not be expected to
work without it: nothing but HTTP and TLS says the name it dialled. That
limitation is the subject of [ADR-019](docs/adr/019-service-addresses.md).

**Then:** point the application at `::` instead of loopback and restart it.
`status` should show the service as `held by another process (not verified)`
rather than as an error — the daemon must not take a port from an application
that binds it. "Not verified" because nothing checks that the process holding
the port is the application: any local process that bound it first would be
listed the same way.

---

## T11 — a fresh machine, from nothing

The install path, run as a stranger would.

**Do:** on a clean host, `install.sh prepare`, then redeem an invite from a node
already on the mesh — `sudo shrooms join <TOKEN>`.
**Pass:** it appears in every other node's `status` within seconds, is reachable
by name, and survives a reboot.

**Watch for:** anything requiring knowledge not in the README. That is the
actual test.

A host that has run this before is not a clean host, and the difference is
usually invisible: an identity in `/var/lib/shrooms` makes it a *returning*
device, a cached image skips the pull, and a leftover `/etc/hosts` block makes
names resolve before anything has registered them. `scripts/uninstall.sh
--purge` puts the machine back to before, which is what makes this test worth
running twice.

```console
$ sudo ./scripts/uninstall.sh --purge --yes
```

---

## T12 — the first mesh, minted in a container

The other half of T11, and the one nothing else covers: the machine that
*creates* the mesh when the binary is in an image rather than on the host.

**Do:** on a clean host, `sudo bash install.sh init --mesh home --name a`. Write down the
recovery key. Then `sudo systemctl restart shrooms`, `sudo shrooms invite`, and
redeem the token on a second host prepared with `install.sh prepare --name b`.

**Pass:** `admin-home.json` (one file per mesh, named by its label) is in the
invoking user's `~/.config/shrooms` on the **host** — not root's, and not
inside the container — and is still there after the restart. `invite` finds it, b enrols, and each appears in the other's
`status`.

**Watch for:** the admin key written into the container. That has no symptom at
all until the first invite, by which time the `--rm` container holding it has
been replaced and the mesh can never admit another device. The wrapper runs
`init`, `invite`, `admin` and `keycard` in a sibling container for exactly this
reason, so `shrooms invite` — through the wrapper, not `docker exec` by hand —
is what this test has to use.

**Then:** on that same prepared-but-not-yet-joined machine, check the other
order too. `sudo bash install.sh init --mesh home --name a` — no `--force` — must mint a
mesh into the config `prepare` wrote, and the name, port and relay setting from
that config must survive it: set `--relay` during `prepare`, leave it off during
`init`, and check it is still on afterwards. On a host that is already *in* a
mesh the same command must refuse rather than mint a second one.

**And names, which nothing inside the container can arrange.** `ping6 b.home.mesh`
from a, with no manual step in between — and on **b**, the machine that was
`prepare`d and then joined, which is the case that broke: the join re-execs the
daemon in place, so no systemd event marks the moment names became answerable.
`shrooms-resolved.service` should register within ~30s of the join without
anyone restarting anything, and its log should name the interface and address.

Then `sudo systemctl restart shrooms` and ping again — the tun is new and
resolved forgets the old one. `sudo systemctl stop shrooms` should revert,
leaving no link settings behind in `resolvectl status`. And while b sits
prepared but unjoined, its journal should stay quiet: no `podman exec` every
thirty seconds to be told there is still no mesh.

**Watch for:** `shrooms status` claiming `names !! not resolving here` when they
do resolve, or staying silent when they do not. It reads that from a marker the
host-side unit drops in `/run/shrooms`, which is the only place the two sides
can both see.

---

## T13 — a LAN host

[ADR-068](docs/adr/068-a-member-can-speak-for-a-lan-host.md). Needs a mesh
with an admin, a gateway member on a LAN, and a device on that LAN that runs no
shrooms — a printer, or `python3 -m http.server 8080` on another machine.

**Do:** on the gateway, `mesh.home.lan_hosts = ["web=192.168.1.20/tcp:8080"]`,
restart; then, where the admin key is,
`shrooms admin lan grant --mesh home --name <gateway> --host web --ports 8080`.
**Pass:** every member's `status` lists it under *LAN hosts* as `via <gateway>`,
and `curl http://web.lan.<gateway>.home.mesh:8080` returns the page from another
member, the phone on mobile data included. A port the grant does not name gets
no answer at all, not even a reset.
**Then:** withdraw the grant. An open connection closes at once and the name
stops resolving. Stop the gateway: status says `(gateway offline)`.

**Watch for:** a service written to forward to a LAN address
(`ha:80->192.168.1.20:80`). It must be refused, with what to write instead —
services forward to this machine only.

---

## T14 — a config the daemon refuses

[ADR-080](docs/adr/080-the-daemon-starts-on-the-last-config-that-did.md).

**Do:** on a running node, break the config — `listen_port = "abc"`, say — and
`sudo systemctl restart shrooms`.
**Pass:** the node comes back on its meshes, on `config.toml.last-good`;
`shrooms status` opens with `CONFIG REFUSED` and the reason; the broken file is
left as it was. Fix it and restart: the warning goes.

**Watch for:** a node that went off the mesh it is being reached over. That is
what this exists to prevent, and the test is worth running over ssh across the
mesh for exactly that reason.

---

## The automated ones

The scenarios above need real networks. What does not is in the repository:

- `make test-unit`: the packages that need no library, with the race detector.
- `make test`: everything, with liblogosdelivery (`make deps-release`).
- `make e2e`: managing a mesh, offline — minting, admitting, renewing, a node
  that loses its state (`scripts/e2e-management.sh`).
- `make e2e-two-nodes`: two nodes on one machine with a local fleet — mint,
  admit, discover, tunnel. Needs root.
- `scripts/e2e-f03-tunnel-key.sh` and `scripts/e2e-f03-remembered.sh`: a
  device announcing another's WireGuard key is refused, live and from
  remembered state. Need root.

---

## Tearing down

```console
$ sudo ./scripts/uninstall.sh            # the software; identity stays
$ sudo ./scripts/uninstall.sh --purge    # identity and config too
```

`--purge` lists what it found on the machine and asks before removing any of it,
so there is no separate preview to run. It undoes all three install
paths — `make install`, `install.sh` and the portable installer — plus what a
running daemon leaves behind: the managed `/etc/hosts` block and any stranded
`shrooms*` or `logos*` interface — both bases, because a mesh after the first
gets a derived name that is never written to the config. `make uninstall` and
`make purge` are the same two things from a checkout.

**Most of that is testable without a machine to wreck.** `make test-uninstall`
runs the script against a staged tree — `DESTDIR` for every path, `HOSTS_FILE`
pointed at a copy, no root — and checks all three install paths are removed, the
hosts block is stripped without disturbing anything else, and the mesh authority
survives `--purge`. CI runs it. What it cannot cover is the interface sweep,
which needs a real tun: that is what the run below is for.

`--purge` discards the device identity and this device's credential, so the
machine returns as a **new** device with a different overlay address and needs a
fresh invite. Without it the identity stays — which is usually what you want when
testing repeatedly, or every run pollutes every other node's roster with a peer
that never comes back.

What `--purge` never takes without `--admin-keys-too`, including under `--yes`,
is the mesh authority: `/etc/shrooms/admin` and `~/.config/shrooms`. Testing the
install flow on the machine that *minted* the mesh would otherwise end the mesh
rather than reset the node — see
[when a node loses its state](docs/when-a-node-loses-its-state.md).

Peers that vanish drop out after `OfflineAfter` (3 minutes) and are removed from
the data plane once they have no live tunnel.
