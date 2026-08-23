# Services that register themselves, and apps that find them

Vaclav, 2026-08-23: *"can we have a 'register service' interface where
apps/services could register on start with the daemon … and then also discovery
— an app could query shrooms for services and discover a Logos Storage endpoint
and use it over the mesh to back up data?"*

Short answer: yes, and most of it is already paid for. The two halves are
separate problems with separate answers, and the discovery half is nearly free.

## What exists today

Services are **declared by an admin, in config**, as flat strings
(`internal/service`, `ParseSpec`):

    immich:2283                     published and forwarded on the same port
    jellyfin:8096->127.0.0.1:8920   published here, forwarded there
    ha->192.168.0.116:80            something not on the mesh at all

The daemon binds each on the overlay address and forwards. Names are announced
if `announce_services` is on ([ADR-023](adr/023-announcing-services.md)), and
`shrooms status` shows peers' lists. `/config/services` on the control socket
can rewrite them at runtime, in the socket-group tier.

So there is already a service concept, a way to publish one, and a way to
gossip the names. What is missing is (a) an app doing it for itself and (b)
anything an app can *query*.

## The discovery half is nearly free: DNS-SD

This is the part worth being excited about. Shrooms already runs a resolver for
the `.mesh` suffix and **already registers it with the host** — that is the
expensive, fiddly part, and it is done. The resolver answers `AAAA` (and `A` via
the synthetic v4 alias, [ADR-021](adr/021-synthetic-ipv4.md)) and nothing else:
`internal/dns/dns.go` returns early on anything that is not `AAAA`.

[DNS-SD (RFC 6763)](https://www.rfc-editor.org/rfc/rfc6763) is the standard
answer, and it is three more record types on a resolver that already exists:

| record | says |
|---|---|
| `PTR` on `_storage._tcp.home.mesh` | which instances exist |
| `SRV` on `nas._storage._tcp.home.mesh` | host and port |
| `TXT` on the same | key/value metadata — version, path, capabilities |

**Why this is the right shape here.** An app that wants to find Logos Storage
over the mesh does not need a shrooms client library, a socket, permissions, or
any knowledge that shrooms exists. It calls the DNS-SD API its language already
has — `dns-sd`, Avahi's client, `zeroconf` in Go/Python/Rust, `NSNetService` —
and gets back a host and port it can dial. That is a very large difference from
"query shrooms", which would mean shipping a client for every language and
gating it behind socket permissions the app will not have.

It also composes with what is already true: those names resolve to overlay
addresses, so the connection goes over the mesh with no further work.

## The registration half needs a decision

Registration is where the design questions are, and there is a standards-track
answer worth reading before inventing one:
**[SRP, RFC 9665](https://www.rfc-editor.org/rfc/rfc9665)** (Lemon & Cheshire,
June 2025) — DNS-SD registration over **unicast** DNS UPDATE, built for networks
where multicast is a bad idea. Its registration model is the interesting part:

> DNS-SD Service registration uses public keys and SIG(0) to allow services to
> **defend their registrations**.

That is first-claim-wins keyed to an identity — which is exactly the pattern
this project already chose for blind relay registration, for the same reason.
Whatever we build, that is the conflict model to copy.

Three shapes for the local interface, and they are not exclusive:

**A drop-in directory** — `/etc/shrooms/services.d/*.toml`, watched by the
daemon. A package or a container bind-mount drops a file and the service exists.
No new authentication model at all: file permissions already decide who may
write there, which is the same mechanism the control socket uses and the one the
August audit found was the *real* boundary. Works for anything that can write a
file, which is everything. Does not handle "register while running, deregister
on exit".

**The control socket**, extending `/config/services`. Runtime registration, and
already built as far as the transport goes. But the audit is relevant: that tier
can already make the root daemon forward to any address the host can reach, and
an app registering itself should not need that much power. Would want a
narrower endpoint that can only add a service pointing at a port on loopback,
and even then the caller has to be in the socket group — which an ordinary app
is not.

**Systemd socket activation / unit metadata** — the daemon reads what units
declare. Neat on a systemd host, useless on the phone and in containers.

## The questions worth deciding before building

1. **Who may register, and what stops a squatter?** The audit already found that
   any local process which binds a service port first is reported as "the
   application itself". Registration makes that easier, not harder: a local
   process could claim `storage` and receive whatever the mesh sends it. SRP's
   answer — a key that defends the registration — is the one to copy, and the
   mesh already has per-device keys to build it from.

2. **What happens when two devices claim the same service name?**
   `internal/hosts` already refuses to resolve an ambiguous *device* name and
   deliberately does not pick a winner. Services multiply that: two machines
   both running Immich is normal and reasonable. DNS-SD's answer is that the
   instance name disambiguates (`nas._immich._tcp` vs `pi5._immich._tcp`) and
   browsing returns both, which is better than anything a single flat name can
   do.

3. **Liveness.** A registered service that stops must stop being advertised, or
   discovery returns addresses that refuse connections and the feature is worse
   than nothing. TTL plus re-registration is the standard answer; the announce
   cadence already gives us a heartbeat to hang it on.

4. **What does this cost the announce budget?** Service data rides the control
   plane, which is padded and finite — see `TestEndpointBudget`. TXT records are
   where metadata sprawls. Worth deciding early whether TXT is for a handful of
   short keys or a general bag, because it will fill whatever it is given.

5. **Is cross-application discovery in scope at all?** "An app discovers Logos
   Storage and backs up over the mesh" is [ADR-030](adr/030-tailscale-shaped-not-tor-shaped.md)'s
   second open question — shrooms as transport for other Logos applications —
   which is recorded as still open and compatible with the current direction. It
   would be the first feature aimed at a consumer that is not a person at a
   terminal.

## What Logos Storage actually needs, having looked

Logos Storage is Codex, renamed in April 2026
([logos-storage/logos-storage-nim](https://github.com/logos-storage/logos-storage-nim)).
The check was worth doing, because it already has a discovery mechanism — and
it turns out to be a different one from the one this note is about.

**It discovers content, not neighbours.** CODEX-DHT is a Kademlia DHT that maps
content IDs to the providers holding them, across the whole network. That is the
question "who has this dataset", and shrooms has nothing useful to add to it.

**It has no local-node discovery at all.** A node exposes a REST API on
`http://localhost:8080/api/storage/v1/` — `GET /peerid`, `GET /spr` for its
Service Peer Record, `GET /connect/{peerId}` which takes supplied multiaddrs,
and the data endpoints. Nothing in it answers "which machine near me is running
one, and on what port". That is exactly the gap.

So the two are complements rather than alternatives, and the integration is
concrete enough to describe:

    _storage._tcp.home.mesh.internal        PTR   nas._storage._tcp....
    nas._storage._tcp.home.mesh.internal    SRV   0 0 8080 nas.home.mesh.internal
                                            TXT   spr=... peerid=... api=/api/storage/v1

An app then resolves that with a stock DNS-SD library, reaches the API over the
mesh, and — the interesting part — hands the peer's SPR to `/connect/{peerId}`
so two of *your* storage nodes peer **over the overlay**: stable addresses,
already authenticated, no NAT traversal and no dependence on the public DHT
finding them. That is a real thing shrooms provides that Codex's own discovery
does not, rather than a duplicate of it.

One practical snag: 8080 is a crowded port. `storage:8080` as a service spec
will collide on any machine already serving something there, and the audit's
finding applies — the daemon reports a port it did not bind as being held by
"another process" and does not check which.

## The case that prompted this: a phone with no storage node

Vaclav, clarifying: *"there is no Logos Storage for mobile, but if I have a
desktop node on the mesh I can use the REST API easily and securely — I just
need to find it with minimal config."*

That is a much narrower thing than general service discovery, and most of it is
already built. Worth separating what exists from what does not.

**Already true.** A peer's announce carries `Bound` as `"name:port"`
([ADR-026](adr/026-bound-ports.md)), the phone already receives it, and
`mobile/support.go` already surfaces it per peer. So a desktop that binds
something on its mesh address is already discoverable from the phone, port
included, with no new protocol at all.

**Already true, and the reason this is worth doing at all.** "Securely" is
carried entirely by the mesh: the API is reached over WireGuard, between two
devices that hold credentials this mesh's admin signed. No TLS to configure, no
certificate, no port forwarding, no exposure beyond the mesh.

**The gap.** Logos Storage binds its API to `localhost:8080` by default, so the
natural declaration is a *forwarded* service — `storage->127.0.0.1:8080` — and
**forwarded services announce their name and not their port.** `publishServices`
puts `sp.Name` into `Names`, and nothing carries the published port. The phone
learns that `nas` offers `storage` and has to be told `8080` by hand, which is
exactly the config the request wants to avoid.

Two ways out, and they are worth deciding between rather than doing both:

1. **Bind the mesh address instead of loopback.** If Logos Storage can be told
   to listen on the device's overlay address, it becomes a `Bound` service,
   `name:port` is already announced, and the phone already has everything. Zero
   new code — a documentation answer rather than a feature.
2. **Announce the port for forwarded services too.** `Names` cannot simply
   become `name:port`: an older reader would take the whole string as a name and
   `sanitiseName` would mangle it into a DNS label that resolves to nothing. So
   it wants a new field alongside, tolerated when absent, in the shape ADR-026
   already established.

Option 1 is free and should be checked first. Option 2 is small, and is right
regardless, because "the port a service is published on" is not knowledge a
client should have to be given out of band.

**One thing to say out loud before either.** The Logos Storage API is
unauthenticated — it binds loopback because that *is* its access control. On the
mesh it gains `DELETE /data/{cid}` for every member. On a mesh where every
device is yours that is fine and is the same trust the tunnel already assumes.
On a mesh with a guest it is not, and nothing in shrooms would stop it.

## Peer discovery over mesh addresses

Vaclav's second half: *"using mesh addrs for peer discovery could be useful to a
degree."*

Agreed, and "to a degree" is the right hedge. `GET /spr` returns a node's
Service Peer Record and `GET /connect/{peerId}` takes supplied multiaddrs, so
two of your own storage nodes can be pointed at each other over the overlay:
stable addresses, already authenticated, no NAT traversal, no waiting for the
public DHT to find them.

Where the degree runs out: this only helps nodes that are *yours*. Codex's DHT
exists to find providers you have never met, which is most of them, and nothing
here replaces that. So the honest framing is that the mesh is a good way to
introduce your own nodes to each other and irrelevant to everyone else's — a
private peering shortcut, not a discovery layer.

## How others do it

- **mDNS / DNS-SD (Bonjour, Avahi)** — the default on every LAN. Apps register
  through a local daemon over D-Bus or a C API; browsing is multicast. The data
  model is the part to take; the multicast transport is the part to leave, since
  a mesh has no broadcast domain.
- **SRP (RFC 9665)** — the same data model over unicast, with keyed defence of a
  registration. Designed for Thread and low-power networks, and a close fit for
  an overlay where there is no multicast either.
- **Tailscale Services** (beta through 2026) — each service is a resource with
  its own virtual IP; hosts advertise endpoints matching it, auto-advertising on
  startup. Two things to note: the service is defined *centrally* in the tailnet,
  which needs the coordination server this project does not have; and hosts
  reject packets to service IPs on ports they do not advertise, which is a
  cleaner story than our "something else already has the port" case.
- **Consul, etcd** — a registry as a service. Needs a coordinator; against
  everything this project is for.

## Recommendation

**Split it, and do discovery first.** SRV/TXT/PTR on the existing resolver is a
contained change to one file, needs no new interface, no permissions model and
no client library, and immediately makes every service already declared in
config discoverable by ordinary apps. It is useful on its own even if
registration is never built.

**Then registration, starting with the drop-in directory**, because it needs no
new authentication model — file permissions are already the boundary — and
covers packages, containers and anything that can write a file. A socket API for
"register while running, deregister on exit" is a later, narrower endpoint, and
should be narrower than `/config/services` is today.

**Copy SRP's conflict model rather than inventing one**, and note it is the same
first-claim-wins-keyed-to-an-identity that blind relay registration already uses.
