# Services that register themselves, and apps that find them

Vaclav, 2026-08-23:

> A storage node (or some adapter) could notice "oh there is a shrooms daemon"
> and register `logos-storage:addr:port`, and then another app (basecamp,
> android) could query "do we have logos-storage on the mesh?" and get a URL
> back.

Two halves — registration and query — and the prompting case is concrete: there
is no Logos Storage for mobile, but a desktop node on the mesh can serve its
REST API to the phone. The only missing piece is finding it without
hand-configuration.

## Most of this already exists

Worth separating what is built from what is not, because it is less than it
looks.

**The transport.** A peer's announce already carries `Bound` as `"name:port"`
([ADR-026](adr/026-bound-ports.md)), the phone already receives it, and
`mobile/support.go` already surfaces it per peer. Services declared in config
are already published, forwarded, and announced.

**The security, which is the whole reason this is worth doing.** An API reached
this way runs over WireGuard between two devices holding credentials this mesh's
admin signed. No TLS to configure, no certificate, no port forwarding, no
exposure beyond the mesh. That is not a feature to build; it is what the mesh
already is.

**What is missing is one thing: a type.**

## Types, not nicknames

This is the whole design, and today's model does not have it.

Service names now are **nicknames** — `immich`, `ha`, `jellyfin` — chosen by
whoever wrote the config, meaningful to that person, agreed with nobody. They
work because a human reads them.

"Do we have logos-storage on the mesh?" is a different question. It only works
if `logos-storage` means the same thing on every device and to every
application, which makes it a **type**: allocated once, agreed in advance, never
chosen per install.

A registration is a triple. The announce carries two of the three:

| | | |
|---|---|---|
| **type** | `logos-storage` | well known, agreed, queryable | **missing** |
| **instance** | `nas` | which device | the roster, already |
| **endpoint** | `addr:port` + path | where | `Bound` has it; `Names` does not |

**Ports can be detected; types cannot.** `boundPorts` reports what is listening
via `listeners.On`, and a listener's name comes from `WellKnown(port)` or falls
back to `port-<n>`. Bind Logos Storage on the overlay address today and a peer
learns that `nas` has `port-8080:8080` — that something is there, and nothing
about what. Only the process itself knows what it is, which is exactly why it
has to be the thing that says so.

## What the prior art settles

This is a well-trodden problem, and three families have solved it. Encouragingly
they all converged on the shape proposed above: register with the **local**
daemon, let it handle propagation.

**Type allocation is not ours to invent.** DNS-SD types come from IANA's Service
Name and Transport Protocol Port Number Registry
([RFC 6335](https://www.rfc-editor.org/rfc/rfc6335)): at most **15 characters**,
letters, digits and hyphens. `logos-storage` is 13 and fits. There is a
registry, it is the one every other DNS-SD service uses, and joining it is a
form somebody fills in.

**Registration semantics: copy Avahi.** It already has the two-track split —
`/etc/avahi/services/*.service` for static things, and a **D-Bus `EntryGroup`
API** for running processes. The second has a property a config file cannot
give: *the registration is tied to the connection*, so a crashed service stops
being advertised without a timeout having to notice. That answers the liveness
question better than TTL-and-re-register does.

**Conflict resolution: copy SRP** ([RFC 9665](https://www.rfc-editor.org/rfc/rfc9665)).
DNS-SD registration over unicast, with SIG(0) keys so a service can *defend* its
registration. Built for Thread and Matter, which have the same no-multicast
problem an overlay does. It is also the same first-claim-wins-keyed-to-an-identity
that blind relay registration here already uses.

**Registry-with-agent — Consul, Kubernetes, etcd, Eureka** — all need a
coordinator and are therefore out. But `consul services register` targets the
*local agent*, which then syncs, so the microservice world landed on the same
half of the pattern. Health checks being first-class there is worth noting.

**Tailscale Services** (beta through 2026) gives each service its own virtual IP
and auto-advertises on startup. The service is defined *centrally* in the
tailnet, which needs the coordination server this project does not have.

**mDNS/SSDP/WS-Discovery** are the multicast-shaped ancestors. A mesh has no
broadcast domain, so the data model transfers and the transport does not.

## What is actually left to decide

**1. Who may register a type, and what stops a squatter?** This is the one real
problem. A type collision is worse than a nickname collision: registering
`logos-storage` is a claim to *be* Logos Storage, and the mesh would then send
storage traffic wherever a local process said. The August audit already found
that a process which binds a service port first is reported as "the application
itself" without anything checking — registration makes that easier unless the
keyed model comes with it.

**2. Which interface?** Not exclusive, and they answer different needs:

- **A drop-in directory** (`/etc/shrooms/services.d/`) needs no new
  authentication model — file permissions are already the boundary, which the
  audit found is the real one anyway. Covers packages, containers, anything that
  can write a file. Cannot express "deregister on exit".
- **A socket endpoint**, narrower than `/config/services` is today. That tier can
  already make the root daemon forward to any address the host can reach, which
  is far more than a self-registering app should need.

**3. How the query is answered — and this is smaller than it first looked.**
Three callers want three different things:

- **Basecamp** already talks to the control socket. A read-only
  `GET /services?type=…` is small, and read-only is the right tier.
- **The Android app** is in-process with the binding. A Go function call. There
  is nothing to design.
- **A third-party app** has neither, and that is where DNS-SD earns its place.

So DNS-SD is the *third* case, not the first. Both of the front-ends that exist
today are served by an endpoint and a function, which makes the first useful
version much smaller than "add SRV/TXT/PTR to the resolver".

Returning a **URL** rather than a host and port matters: the path is part of
finding the thing (`/api/storage/v1`), and every caller would otherwise hardcode
it.

## Recommendation

**Do the type first, and nothing else.** Carry a type alongside the name in the
service announce, populated from config. That alone makes "do we have
logos-storage on the mesh?" answerable from data the phone already receives, and
is useful before any registration interface exists.

**Then the query, for the two callers that exist** — a read-only socket endpoint
for Basecamp and a binding function for Android. Both are small and neither
needs a new trust model.

**Then registration**, starting with the drop-in directory for the reasons
above, with the keyed defence from SRP designed in rather than added later. A
connection-tied socket API, with Avahi's semantics, when something actually
needs to deregister on exit.

**Leave DNS-SD until a third-party app wants it.** It is the right answer for
that case and an expensive way to serve two callers who already have better
ones.

## The worked example

Logos Storage is Codex, renamed April 2026. `storage/conf.nim` has
`--api-bindaddr` (default `127.0.0.1`), `--api-port` (default `8080`) and
`--api-cors-origin`.

Its own discovery is CODEX-DHT, a Kademlia DHT mapping content IDs to the
providers holding them — the question "who has this dataset", across the whole
network. It has no local-node discovery at all: `GET /peerid`, `GET /spr` and
`GET /connect/{peerId}` all assume you already know where the node is.

So the two are complements. The DHT answers the global question; the mesh
answers "which of my machines runs one, and how do I reach it".

**Say this out loud before pointing a phone at it:** the Logos Storage API is
unauthenticated — it binds loopback because that *is* its access control. On the
mesh it gains `DELETE /data/{cid}` for every member. Fine where every device is
yours, which is the same trust the tunnel already assumes. Not fine with a guest
on the mesh, and nothing in shrooms would stop it.

## Peer discovery over mesh addresses

Vaclav's second half: *"using mesh addrs for peer discovery could be useful to a
degree."* Agreed, and "to a degree" is the right hedge.

`GET /spr` returns a node's Service Peer Record and `GET /connect/{peerId}`
takes supplied multiaddrs, so two of your own storage nodes can be pointed at
each other over the overlay: stable addresses, already authenticated, no NAT
traversal, no waiting for the public DHT to find them.

Where the degree runs out: this only helps nodes that are *yours*. The DHT
exists to find providers you have never met, which is most of them. The honest
framing is a private peering shortcut, not a discovery layer.
