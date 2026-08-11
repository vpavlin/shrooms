# 015. Multiple meshes in one daemon

**Status:** accepted; built and in daily use.

Per-mesh identity is derived from one master secret, each mesh gets its own
WireGuard device rather than one TUN carrying several prefixes, and
credentials, relay selection, services and the synthetic IPv4 block
([ADR-021](021-synthetic-ipv4.md)) are all per mesh. Names resolve across the
meshes a node belongs to. Running now on a laptop and a phone with two meshes
each, plus a VPS and two LAN machines.

**The `mesh_id` collision is settled**, in favour of
[ADR-018](018-credentials-instead-of-a-shared-key.md): a mesh is named by the
hash of its admin public key, and the address prefix derives from that
(`cred.MeshID.Prefix`), not from the network key as this ADR originally had it.

## Context

Alice has a mesh for her own machines. Bob has one for his. They also want a
shared mesh so Bob can reach Alice's NAS, without either gaining access to the
other's private mesh.

The cryptography already supports this cleanly. Everything is derived from the
network key — the /48 prefix, the rendezvous topics and their payload key, the
pairwise WireGuard PSKs, the disco key, the relay key — so two meshes are
independent by construction, with no shared mutable state to get wrong.

Running one daemon per mesh therefore almost works today. It was rejected: it
multiplies configs, state directories, TUN interfaces, ports, control sockets
and Logos Messaging nodes, and the last of those is the expensive one. The
requirement is that `shrooms join` can be run more than once and that be the
end of it.

## Decision

**One daemon, one messaging node, many meshes — but a WireGuard device, a TUN
and a UDP port each.**

The original line here was "one TUN, one UDP port", and building it showed that
to be wrong in a way worth recording rather than quietly changing.

A wireguard-go `Device` has exactly one static private key, and the whole point
of the per-mesh identity below is that the key differs per mesh. Worse,
WireGuard allows **one preshared key per peer**, and our PSK is per mesh — so a
peer that shares two meshes with us cannot be one entry on one device at all.
Two devices are therefore forced.

Two devices cannot share a UDP port either. A transport packet identifies its
destination only by a receiver index the receiving device allocated, so nothing
in the packet says which device it belongs to. Handing every packet to both and
letting the wrong one fail authentication would work — everything is
authenticated — but it doubles the work on the data path and turns other
meshes'' traffic into a stream of decrypt failures in the log. Boring and
separate beats clever and shared here.

What that costs is one NAT mapping per mesh rather than one shared, so hole
punching no longer amortises across meshes. What it keeps is the expensive
thing: **one Logos Delivery node**, whose ~20 MB/h dwarfs everything else a node
does (see the bandwidth measurements in the README). Running a daemon per mesh
would have duplicated that, which is what this ADR set out to avoid.

Ports are allocated from `listen_port` upwards in label order, so a config that
names one mesh listens exactly where it always did.

### Per-mesh identity, derived from one master secret

```
device_key(m) = HKDF(master_secret, "mesh/v1/identity" || mesh_id)
wg_key(m)     = HKDF(master_secret, "mesh/v1/wg"       || mesh_id)
```

This is load-bearing, not hygiene. WireGuard identifies peers by public key
within a device and allows **one preshared key per peer**, while our PSK is
per-mesh. A device that shares two meshes with us therefore cannot be
represented twice on one WireGuard device — unless it presents a different
public key in each, which per-mesh derivation gives for free.

It also closes a linkability leak. `OverlayAddr` mixes the network key into the
prefix but **not** into the host bits, which are `SHA256("mesh/v1/addr" ||
device_pub)`. A device reusing one identity therefore carries the same 80-bit
suffix in every mesh it joins, so anyone in two meshes can correlate devices
across them. The same is true of `DevicePub` in the announce. Per-mesh
identities remove both, and mean joining a shared mesh reveals nothing about
membership of a private one.

`mesh_id` is derived from the network key, not chosen, so it needs no agreement:

```
network_id = SHA256("mesh/v1/meshid" || NK)[0:8]
```

**Renamed from `mesh_id`, because that name was taken.**
[ADR-018](018-credentials-instead-of-a-shared-key.md) introduced a *different*
mesh id — the hash of the sorted admin key set — which is what a credential
names and what says which authority admitted you. Two identifiers with one name,
one of which the address prefix depends on, is a bug waiting for a bad afternoon.

They are not the same thing and neither can replace the other:

| | derived from | exists when | answers |
|---|---|---|---|
| **network id** | the network key | always | which mesh is this, locally |
| **mesh id** (ADR-018) | the admin key set | only with an authority | who may admit devices |

A mesh with no `admin_keys` — which is every mesh today, and which stays
supported — has no ADR-018 mesh id at all, so the network id is the only one
that can key per-mesh derivation. Nothing about this is visible on the wire:
the address prefix already derives from the network key (ADR-005), and this
identifier is a local label over the same secret.

### Mesh names are local labels

```
shrooms join <KEY> --mesh shared
```

Optional; defaults to a short rendering of `mesh_id`. Names are **local to the
node**, and deliberately not carried in the announce.

There is no authenticated channel to distribute a mesh's name. Putting it in the
announce would make it self-asserted, exactly like device names (ADR-008), so
any member could rename the mesh for everyone else. Alice calling it `shared`
while Bob calls it `alice-nas` is not a defect — it is the only thing that can
be true without an authority. `mesh_id` is the identifier; the name is a label
over it.

Names must be unique on the node, and `join` rejects a collision.

### Naming: `$peer.$mesh.$suffix`

`vps.mesh` is ambiguous the moment two meshes contain a `vps`. So the qualified
form is canonical:

```
vps.home.mesh
nas.shared.mesh
```

The short form `vps.mesh` is **also** emitted, but only when that peer name is
unambiguous across every joined mesh. A single-mesh node — which is every node
today — therefore sees no change, and ambiguity degrades by removing the short
name rather than by silently resolving it to one of the candidates.

This is the same rule already applied to duplicate device names within a mesh
(`hosts.Render` appends a piece of the address rather than letting the last
writer win), extended one level out.

### Configuration

Meshes are flat, prefixed keys rather than TOML tables:

```toml
mesh.home.key    = "P27KNQ2..."
mesh.home.relay  = "true"
mesh.shared.key  = "D4R5TBD..."
```

The config parser is a hand-rolled TOML subset with no table support. Prefixed
keys need no parser rewrite and give per-mesh options for free. The single-mesh
`network_key = "..."` form stays valid and means one mesh named `default`.

### State

`state.json` grows a per-mesh map holding the derived identity and, critically,
the announce sequence number — `Seq` must persist per mesh, since it is per
device *per mesh* that peers' replay guards track.

**Existing state is migrated by keeping its keys, not by re-deriving them.** A
node that regenerated its identity would change its overlay address and
WireGuard public key, breaking every established tunnel and appearing to its
peers as a new device while the old one lingered until it timed out. The
existing identity is written under the existing mesh's id verbatim.

### Credentials are per mesh

Each mesh carries its own `admin_keys`, its own credential in the announce, and
its own revocation list. Nothing is shared between meshes but the device, which
is the point: being admitted to Bob's shared mesh says nothing about Alice's
private one, and an admin who can revoke you there cannot touch you here.

Since identities are per mesh, a credential names the per-mesh device and
WireGuard keys. That falls out for free — those are the keys the announce is
signed with in that mesh — but it means a credential issued for one mesh is not
merely untrusted in another, it does not even name the right device.

### The network key path stays

`network_key = "..."` remains valid and means one mesh named `default`, and a
mesh with no `admin_keys` keeps admitting anyone who holds its key. Not as a
transitional courtesy: it is how a mesh is bootstrapped, how one is recovered,
and the only thing that works when nothing is running on the other end. Invites
and credentials are the better path and are not yet the only path — auto-renewal
is not built, so a credentialled mesh still needs a human every thirty days.

Concretely, nothing in this ADR may require an authority to exist.

### The waiting daemon is the runtime half

A daemon that gains a mesh while running is what "join more than once" means,
and it is [already built](../../cmd/shrooms/waiting.go) for the empty case: a
daemon with no key holds the control socket and takes a mesh over `/join`. The
remaining work is that the same call must be accepted by a daemon that already
has one, and must add rather than replace.

## Consequences

**Joining a mesh does not bridge it to another.** Each node is an endpoint, not
a router: there is no IP forwarding, and `AllowedIPs` bounds what each peer may
send. Bob cannot reach Alice's private mesh through a machine that is in both
unless she deliberately enables forwarding. This is the property the whole
arrangement rests on and it is worth stating explicitly, because the obvious
fear — "I joined a shared mesh and exposed my home network" — is not what
happens.

**One NAT mapping serves every mesh.** Sharing the socket was already required
for traversal (ADR-002); the side effect is that hole punching and reflexive
discovery amortise across meshes rather than being repeated per mesh.

**Rendezvous cost is sub-linear.** All meshes' topics hash to the same shard
(spike S3), so N meshes add N×3 content-topic subscriptions but no additional
gossipsub mesh.

**Inbound control packets must be tried against each mesh's keys.** Announces,
disco probes and relay frames carry no cleartext mesh identifier — deliberately,
since one would be a membership oracle for a passive observer. Demultiplexing is
therefore trial decryption, O(meshes) per packet. Fine at the scale this is for;
if it ever isn't, the fix is a per-mesh listen port, which costs privacy.

**Every mesh needs its own IPv4 aliases.** [ADR-021](021-synthetic-ipv4.md)
gives each peer a synthetic address derived from its device key. With per-mesh
identities that derivation is already distinct per mesh, so the aliases do not
collide by construction — but the *table* must be per mesh, or one mesh's peer
would answer another's name.

**Sharing a mesh key still shares everything.** The network key is a bearer
credential (ADR-008): giving Bob the key to mesh C makes him a full member who
can enrol further devices, with no revocation until M5. "Let Bob reach this one
service" is not expressible and is squarely M5 work. Accepted deliberately —
this is the position today, and multi-mesh at least contains the blast radius to
one mesh rather than requiring Alice to share her private one.

## Staging

1. **Mesh naming and per-mesh hosts blocks.** Independently useful: the
   `/etc/hosts` marker is currently a fixed string, so two writers fight.
2. **Config and state** — prefixed keys, per-mesh identity, migration.
3. **Runtime** — a mesh instance per mesh: its own WireGuard device, TUN and
   port, sharing the rendezvous node, the control socket and the resolver.
