# 068. A member can speak for a named LAN host, never for a LAN

**Status:** accepted 2026-10-21 by the owner; built the same day (`internal/cred`
LanGrant, `internal/lanhost`, `internal/lanproxy`, `shrooms admin lan`). Amended
the same day after the auditor's review (`08-adr-068-review.md` in the audit
folder) and the owner's answers to it: normal cages stay network-open, and
services become local-only when LAN hosts ship. Amended again the same day
after the auditor's implementation audit (`09-adr-068-implementation.md`):
the sentences below now say what the code enforces, and members gained LAN
aliases for the printer's passive FTPS. Its re-check
(`10-adr-068-fix-review.md`) closed those findings and its own F-39 to F-46
are folded in below. Narrows the "subnet routing" in `docs/an-exit-node-for-the-mesh.md`,
and doesn't change ADR-030's "no exit nodes".

## Context

Václav wants to print from home to a Bambu Lab printer in the office, in LAN
mode, without the cloud (2026-10-21). The printer can't run shrooms; neither
can most cameras, NAS boxes and other appliances he would want reachable. A
Bambu client needs:
- TCP 8883 (MQTT over TLS);
- TCP 990 (implicit FTPS) plus passive-mode data connections on high ports;
- the camera on TCP 6000 or RTSP 322.

So one forwarded port per service (ADR-023) isn't enough.

The general answer elsewhere is a subnet router: one member advertises
`192.168.10.0/24` and the whole mesh reaches the LAN. That was rejected here,
for what it would expose:
- **Every member reaches everything on the LAN.** That includes things never
  meant to be reachable (router admin pages, appliances with weak auth), and
  every agent session on every member machine. A prompt-injected agent could
  scan the office.
- **It links the mesh to a place.** Advertising a prefix tells every member what
  network sits behind which device, and lets them map it. Announces are
  encrypted, so outsiders don't see it, but every member does.
- **The LAN can't tell members apart.** Everything arrives from the gateway's
  address.
- **Address clashes.** Home and office LANs often use the same range.

Devices that *can* run shrooms stay members: each has its own keys, can be
revoked on its own, and is reached end to end with no gateway in the middle.
This is only for devices that can't.

### What the grant can and cannot contain

The review's first finding sets the honest frame:
- **A gateway already has its LAN.** A compromised one can point a granted
  name at any LAN address and port, and members can't check, since they never
  see the LAN address.
- **Any member can already proxy its LAN without a grant.** Today's `services`
  forwards to any `host:port` (`internal/service/service.go`: `Target`, e.g.
  `nas:80->192.168.1.5:8080`), and the target isn't announced. Any program a
  member runs could do the same.

So the grant is not a containment boundary. **The gateway is trusted for its
own LAN.** What this design adds is:
- an admin's say-so before a LAN host is *named and offered* to the mesh;
- a narrow, default-deny way to offer it;
- a refusal everywhere else.

## Decision

**A member can offer named LAN hosts, one at a time: each with its own mesh name
and address, its own list of ports, and a grant signed by the mesh admin. Every
member can reach them; sealed cages can't. This is the one way a LAN device
reaches the mesh: services become this machine's own ports only.**

### The host

- **One host, never a prefix.** A LAN host is one LAN address behind one
  gateway member, in the gateway's config:
  `mesh.office.lan_hosts = ["printer=192.168.10.42/tcp:8883,990,6000,50000-50100"]`.
  There is no way to route a range of LAN addresses.
- **The LAN address is a literal, checked.** An exact IPv4 or IPv6 literal,
  never a name (no DNS, so no rebinding). It must be inside the subnet of one
  of the gateway's LAN interfaces, checked each time it is dialled, and must
  not be:
  - loopback, link-local, unspecified, multicast or broadcast;
  - its subnet's network or broadcast address;
  - one of the gateway's own addresses;
  - a mesh or synthetic range (ADR-005, ADR-021).

  The address must be private: RFC 1918 IPv4, or IPv6 in fc00::/7 outside the
  mesh's own fd00::/8, so in practice IPv4. A WAN is a physical link too, and
  this is what keeps its neighbours, the ISP's router and public hosts out
  (audit F-40). A LAN interface is decided by type, not name: a physical one,
  or a bridge, bond or VLAN over one (sysfs); a container on docker0, a host
  behind another VPN and a tunnel are refused (F-30). What remains is a
  private network upstream of the gateway, which its owner is trusted not to
  list, and which status shows by interface on the gateway.
- **Limits.** At most 16 hosts per gateway and 256 ports per host, in at most
  8 ranges. A grant for 1–65535 is the whole device, so it's refused. These
  caps also bound the announce.

### Names and addresses

- **Members never see the LAN address.** A host is announced with a stable host
  ID, taken from its grant, and a derived overlay address. The derivation is
  ADR-005's, from the gateway's identity and the host ID, with a domain
  separator so it can never equal a member's address. A synthetic IPv4 follows
  (ADR-021).
- **A namespace of its own.** LAN hosts are named
  `<host>.lan.<gateway>.<mesh>.mesh`, e.g. `printer.lan.pi5.office.mesh`. There
  is no short form. Neither a member's name (ADR-066) nor a service's name
  (`<service>.<device>.<mesh>`) can take this shape: the resolver answers a
  name with `lan` as its second of three labels only from the grants, before
  anything else. A device or service may still be called `lan`; it simply
  never gets that shape (as built: the shape is reserved, not the label). A
  host may not be called `lan`.

### The grant

- **Its own admin-signed object**, distributed like revocations, not a field in
  the credential: a credential is announced, renewed on its own schedule and
  size-bounded. The grant binds:
  - the mesh;
  - the gateway's device key;
  - the host ID and name;
  - the ports, TCP by definition: the grant has no protocol field, so its
    domain separator grants TCP and nothing else. A UDP grant needs a
    protocol field and a new domain, so no grant signed now can be read as
    one (audit F-37);
  - a serial, not-before and not-after;
  - a domain separator.

  **Not the LAN address, not even as a commitment** (changed when built). The
  review suggested a hash of it. But a private address is brute-forced from
  its hash in moments: a /16 is 65,536 candidates. And the gateway is trusted
  for its LAN regardless (above). So a commitment would hide nothing and bind
  nothing. The grant carries no address, and the mesh never does. That
  doesn't make it secret: a device's own protocol may name it (a printer's
  FTPS does, inside TLS), and a member's LAN alias puts it in that member's
  config.
- **Members verify it.** There is no host announcement: a member lists a host,
  routes its address to the gateway and resolves its name only under a
  valid, unexpired grant naming that gateway, verified against the mesh's
  admin keys. It never sees or verifies the host's LAN address; the gateway
  is trusted for that, and serves only the ports the grant names.
- **Revocable.** Withdrawing the grant, narrowing its ports or letting it
  lapse drops that host or port, including connections already open: the
  gateway closes them at once on a grant change and within five seconds of a
  lapse (audit F-27). A gateway's credential running out drops all its
  hosts from every listing (F-31), and the gateway itself stops serving them
  (F-42). Revoking it drops them on each node as the revocation reaches it:
  prompt where it has arrived, and as slow as its distribution elsewhere,
  which repeats it each epoch (F-41). On a mesh with no admin, the feature
  doesn't exist.
- **Ordered by serial.** A node keeps the highest serial for each gateway and
  host. The admin tool's default serial is the time in nanoseconds, and a
  statement not newer than the one held is refused with the serial to use,
  not reported as done (audit F-28: a withdrawal in the same second as its
  grant once printed "Withdrew" and changed nothing). The admin names the
  gateway by `--device`, or by `--name`, which matches only a device whose
  credential carries that name: never a claimant (F-33), and never a device
  that merely asserts it after the real one was forgotten (F-43). A name is
  not a key; the tool says which key it chose.

### The gateway

- **A per-port TCP proxy, default deny.** The gateway accepts connections to the
  host's overlay address on its granted ports only, and opens its own
  connection to the LAN address. A packet for a port no grant in force opens
  is dropped before the stack, so it isn't even answered with a reset; the
  grant and the caps are checked before a connection costs a goroutine
  (audit F-32). Connections are capped per host and per member.
- **Nothing else reaches the gateway's kernel**, and this holds on every node,
  not just gateways. WireGuard checks a packet's source, never its
  destination, so a member could address a packet to a node's LAN or public
  IPv6 and the kernel would take it: to a local service, or, with forwarding
  on, onward to the LAN or the internet. The audit found it on the gateway
  (F-26); every node did it. Each node now hands its kernel only packets
  addressed to its own overlay address (`wg.SelfOnly`, desktop and phone),
  and the gateway checks the same in its own wrapper. Only TCP, UDP and
  ICMPv6 pass, with no extension header but a fragment header: a routing
  header would otherwise ask the kernel to pass the packet on, refused only
  by a kernel default (F-39). Of ICMPv6, only echo, and errors about this
  node's own traffic to the peer reporting them: two ends of a tunnel have
  no router between them, so an error about anyone else is forged, to reset
  or shrink another member's connection (the re-check's residual). So
  there is no IP forwarding and no broad DNAT, whatever the kernel's
  settings. What is dropped is counted, per mesh, as
  `shrooms_mesh_dropped_not_for_self_total` (F-46).
- **In no kernel namespace at all** (as built). The gateway wraps its data
  plane's TUN device: packets WireGuard delivers for a host's overlay address
  go into a userspace TCP stack (gVisor, through wireguard-go's netstack), and
  the stack's replies are merged back into what WireGuard reads. The address
  is never on any kernel interface, so nothing the gateway binds to all
  addresses is reachable through it. That's stronger than the separate
  namespace the review asked for, and needs no namespace setup.
- **TCP only, to start.** Under a TCP proxy, every connection has a start and an
  end, so limits, logging and refusals are simple. UDP would need a per-flow
  translation table (NAT-style state), and many UDP protocols carry addresses
  inside the traffic (RTP, SIP, discovery), the same problem as FTPS passive
  mode. Multicast and broadcast discovery can't be carried to one host at all.
  The printer doesn't need UDP: MQTT, FTPS and the 6000 camera are TCP, and the
  X1's RTSP runs over TCP. UDP waits for a device that needs it, as a per-flow
  relay under the same port lists and limits. ICMP is not carried.

### Who can reach a host

- **Every member of the mesh.** There is no per-member list. A compromised
  member, or an uncaged session on any member machine, can use the host's
  granted ports. That's the same trust as reaching any member's services today.
- **Sealed cages can't, and nothing new is needed for that.** A sealed cage
  already blocks the LAN and the whole mesh range (ADR-045), and a LAN host's
  address is derived inside that range.
- **A normal cage can, as its machine can.** Caged is network-open by
  definition (ADR-060: "no network limits on it"), so it reaches members'
  services today, and would reach LAN hosts. That's the design, not a gap.
  Limiting the network is what sealed is for. The auditor's review read the
  cage rule against the stricter goal; the owner confirmed this one
  (2026-10-21).
- **A sealed session can't reach a host through another session either, and
  that already holds.** A sealed cage "answers and asks nothing" (ADR-045):
  its proxy refuses everything except updating its own tasks. It can't send
  `ask_agent` or any `/a2a` message, so there's no deputy to fool. A task
  from a normal cage is marked "in a cage", and a session can refuse those
  (`accept_caged`, ADR-044).
- **Visible.** `shrooms status` and both apps list LAN hosts as "printer via
  pi5", with whether the gateway is online.

### LAN aliases (added after the audit)

Some applications use the address a device reports, not the one they were
given. Bambu Studio is told the printer's mesh address, then opens the camera
and the FTPS data connection to the printer's LAN address, which it reads
from the printer. From another LAN that address is either nobody, or
whatever local device has it (audit F-29; seen 2026-10-21: control worked
from home, the camera didn't).

- **A member may alias a LAN address to a host**, in its own config:
  `mesh.office.lan_aliases = ["192.168.10.143=printer.lan.proteus"]`. That
  address is routed into the mesh on this device, as a /32, more specific
  than any LAN's route, and translated to the host's overlay address; the
  replies come back from the address the application dialled.
- **Only while the host is on the mesh.** The route is added while the host
  is listed and its gateway online, and taken away when its grant goes or its
  gateway goes quiet (checked every 30 seconds), so the address belongs to
  the LAN again: a laptop back in the office reaches the printer directly,
  even with the gateway down (F-44).
- **A private IPv4 address only.** A public one would quietly take a piece of
  the internet with it. The gateway's name in it is checked as a name, so an
  alias that could never match is an error rather than silently idle (F-45).
- **Nothing changes for the gateway or anyone else.** The gateway sees the
  same connections to the host's overlay address, under the same grant. The
  alias is local config, so the mesh learns nothing more about the LAN.
- **Desktop only, for now.** Android fixes a VPN's routes when it starts, and
  no phone client has needed it.

**A gateway's `lan_hosts` it cannot use do not stop the daemon** (2026-11-04).
They did: a line it refused, or a LAN stack that would not start, failed the
mesh, and the daemon exited with every mesh down. On k11 one edited line, made
to move Home Assistant from a service to a LAN host, took the machine off the
mesh it was being reached over. Now the mesh comes up without LAN hosts, the
log says why at error level, and `shrooms status` gives the reason for each
host granted to this gateway. The same rule services and LAN aliases already
follow: nothing optional stands between a machine and its own mesh.

## Consequences

- **The printer's FTPS is a real risk without an alias.** The FTPS server's
  passive reply names its own LAN address, and that reply is inside TLS, so the
  gateway can't rewrite it. A client that obeys it, rather than reusing the
  control connection's host, connects to that address on *its own* LAN. If the
  member's home LAN uses the same range, credentials and the file go to
  whatever local device has that address. Bambu Studio does use the LAN
  address, for the camera at least (2026-10-21). So:
  - a member that prints sets a LAN alias for the printer's address (above),
    and its passive data connections go to the printer through the gateway;
  - the grant includes the printer's passive data port (2024 on the office
    printer, seen in its connections; check the model's own);
  - a member without the alias is told to add one by the docs, and nothing
    stops it obeying the reply on its own LAN; an FTPS proxy that terminates
    TLS is the alternative, and isn't planned.
- **Services become local-only, but only when LAN hosts ship.**
  - Today a service may forward to any `host:port`. The owner runs Home
    Assistant on the home mesh that way (a service pointing at a LAN address),
    so Home Assistant doesn't have to join the mesh.
  - Once this is built, a service target must be one of this machine's own
    addresses, and a LAN device is offered only as a LAN host, with its grant.
    `localhost` is dialled as 127.0.0.1 or ::1, never looked up, and each
    connection checks again that the target is still this machine's (audit
    F-34).
  - The restriction lands in the same release as LAN hosts, never before, and
    existing LAN-pointing services are moved to LAN hosts (Home Assistant
    first). A daemon that finds one after the upgrade refuses it and says
    what to write instead.
  - This makes the grant meaningful for shrooms' own features. A member can
    still proxy its LAN with programs of its own; nothing in shrooms stops
    that, and nothing claims to.
- **The gateway is the host's single point of failure**, and its compromise
  opens that host's granted ports, and, honestly, anything else on its LAN it
  chooses to proxy. The grant names the gateway, so moving a host to another
  member is a new grant.
- **The LAN host can't tell members apart:** everything comes from the
  gateway. Acceptable for an appliance; it's why members stay members.
- **Before this existed**, `sshuttle` from one machine to an office member over
  the mesh gives that machine the same reach, and nobody else.
- **Settled in the build:**
  - IPv6 hosts;
  - the synthetic IPv4 block for hosts;
  - how phones reach hosts (ADR-035);
  - one host offered by several gateways;
  - renaming a gateway;
  - renewing grants;
  - logging.
