# An exit node for the whole mesh

Vaclav, 2026-08-22: *"can we have a single exit node for the whole mesh? e.g.
can I dedicate the VPS (or an Akash container even?) as an exit node for all the
traffic and all the peers in the mesh?"*

Short answer: yes, it is buildable, it is a good fit, and it is the one item on
the roadmap that [ADR-030](adr/030-tailscale-shaped-not-tor-shaped.md) explicitly
ruled out. That ADR should be amended rather than quietly ignored — but the
reasoning that produced it does not actually cover what is being asked for here.

## ADR-030 declined a different feature with the same name

The ADR says, flatly, **"No exit nodes. Traffic to the open internet does not
route through peers."** And it calls the decision a hinge: *"An exit node is the
hinge. If it were ever in scope, the rest of that group becomes coherent rather
than premature"* — the group being always-relay mode, onion routing, tunnel
obfuscation, and finding volunteers willing to carry strangers' traffic.

That reasoning is sound for the feature the ADR had in view: **an exit node as
circumvention infrastructure**, where the people running exits carry traffic for
people they do not know, and the people using them are relying on our
engineering to not get them hurt. The ADR's honest paragraph — *"the failure
mode is not 'the VPN is slow', it is a person exposed"* — is about that user.

What is being asked for here is a different thing wearing the same word: **your
own VPS, carrying your own traffic, for your own devices.** No volunteers, no
strangers, nobody relying on our threat model to stay out of prison. That is the
Tailscale exit node, and Tailscale ships it while being emphatically not a
circumvention tool — which is precisely the shape ADR-030 chose to stay in.

The hinge argument does not transfer, either. A private exit for your own
machines does not make onion routing coherent, because the thing that made the
rest of that group coherent was *carrying other people's traffic*, not the
routing change. So this can be in scope without dragging the declined group in
behind it.

**Decision needed:** amend ADR-030 with the distinction (private exit for your
own machines: in scope; volunteer exit for strangers: still out), or write a new
ADR superseding it. My preference is amend — the ADR's core judgement survives
intact and it reads better as a refinement than a reversal.

## What it is actually useful for

Worth being concrete, because "exit node" sounds grander than the daily use:

- **Hostile wifi.** Phone on cafe/airport/hotel wifi routes everything through
  your VPS instead of through whoever runs that access point.
- **A stable egress IP.** Every device appears to come from the VPS. Useful for
  IP allowlists — your own services, a work VPN, a bank that panics at new IPs.
- **Home services while away** without exposing them, if the exit is at home.
- **Not** a privacy win against a determined observer. See the trade below.

## The trade nobody should skip

An exit node sees all your traffic on its way out. Not the tunnel — that is
encrypted to it — but everything *after* it decrypts, which is your ordinary
internet traffic with whatever TLS it already had and nothing more.

So this **moves trust, it does not remove it**: from your ISP or the cafe's
router, to whoever runs the machine the exit peer runs *on*.

To be clear about which machine that is: the exit is your own deployed peer, a
full mesh member holding a credential like any other — not a blind relay, which
could not do this even in principle, since it never holds a network key and
cannot decrypt anything to forward it onward. But your peer still runs on
somebody's hardware. A VPS means your hosting provider; an Akash container means
a provider you have never met, chosen by a marketplace, on a host you cannot
inspect. Either can see what the exit peer emits.

That is a straightforward downgrade for the Akash case, and it should be said in
the UI rather than buried here.

## What it takes to build

Seven pieces. None are research problems; several are fiddly and two fail
silently, which is the real risk.

**1. Advertise it.** A node flags in the roster that it offers egress. One bit,
plus which families (v4, v6, both).

**2. Opt in, per device.** Never automatic. A desktop that silently started
routing through Akash because a peer advertised it would be a bug worth a CVE.

**3. Widen AllowedIPs.** Today `internal/wg/device.go:173` writes
`allowed_ip=%s/128` — one mesh address per peer, hardcoded. The exit peer needs
`0.0.0.0/0, ::/0` instead. WireGuard's crypto-routing is longest-prefix, so the
other peers' /128s keep winning for mesh traffic; only the default falls through
to the exit. This part is genuinely easy.

**4. Route table on the client — the fiddly one.** A default route via the tun
routes the tunnel through itself. The standard fix is wg-quick's: fwmark the
tunnel's own packets, a separate routing table, `suppress_prefixlength 0`, and
the `0.0.0.0/1` + `128.0.0.0/1` split default so it outranks the real one
without deleting it. Well-trodden, but it is the part that strands a remote
machine when it goes wrong — and one of ours is a pi5 reached over the mesh.

**5. Forwarding and NAT on the exit.** `ip_forward`, v6 forwarding, and
masquerade out the WAN interface. That is an nftables/iptables dependency we do
not currently have, or netlink work to avoid it.

**6. DNS.** The leak that makes the whole feature a lie. We already run a `.mesh`
resolver (`internal/dns`); with an exit node the *system* resolver has to move
too, or every name you look up still goes to the cafe's DHCP server. Android is
easier here — `addDnsServer` on the Builder, which `MeshVpnService.kt:261`
already calls. Desktop means resolved/resolv.conf, which is where this gets
ugly.

**7. Kill switch.** If the tunnel drops and traffic silently falls back to the
naked default route, the feature is *worse than not having it*: you believe you
are exiting via the VPS and you are not. Fail-closed is correct. Fail-closed on
a remote box you administer over the mesh is also how you lose the box.

Android, notably, gets 4 and 6 nearly free — `VpnService.Builder.addRoute("0.0.0.0", 0)`
and the OS does the rest. The phone is the main use case *and* the easy target,
which is a rare alignment.

## Why the VPS first and not Akash

The VPS is the better first target, for reasons that are not about the code:

- **Stable IP.** The point of the feature for allowlists.
- **Your account, your terms.** Arbitrary traffic egressing from an Akash
  provider's address puts the abuse complaints on *them*, and providers may
  reasonably prohibit it. Worth reading the terms before pointing a mesh's worth
  of traffic at one.
- **Predictable bandwidth.** The blind relay is cheap because it forwards almost
  nothing. An exit node carries everything, and metered egress is a different
  cost model — the $0.15/month figure will not survive.
- **Trust**, as above.

Akash works *technically* — the client dials the exit over WireGuard, which
NodePort already handles, and egress is outbound-initiated, so no IP lease is
needed. It is a fine experiment later, not the first target.

## Suggested order

1. Amend ADR-030.
2. Advertise + opt in + widen AllowedIPs (pieces 1–3).
3. **Android first** — biggest payoff, least routing pain.
4. Exit-side forwarding and NAT (piece 5) on the VPS.
5. Desktop routing and DNS (pieces 4, 6).
6. Kill switch (piece 7), before anyone calls it finished.

## A smaller cousin worth considering first

**Subnet routing**: a node advertises `192.168.1.0/24` rather than a default
route, so the mesh can reach a LAN behind one peer without that peer carrying
all internet traffic. Same advertise/opt-in machinery, same AllowedIPs widening,
**none** of the DNS, kill-switch or default-route risk. It is perhaps a third of
the work, useful on its own, and it builds most of the plumbing the exit node
needs.

If the goal is to get somewhere useful quickly, this is the better first move.
