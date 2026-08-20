# Architecture decisions

Short records of the decisions that shaped this project, and the evidence behind
them. Each is one page: context, decision, consequences, and what would change
our mind.

Most of these rest on research that would otherwise be lost — measured
throughput figures, NAT prevalence data, source-level findings in nwaku. Where a
number appears, its source is named.

| # | Decision | Status |
|---|---|---|
| [001](001-wireguard-not-libp2p-streams.md) | WireGuard for the data plane, not libp2p streams | accepted |
| [002](002-userspace-not-kernel-wireguard.md) | Userspace WireGuard, not the kernel module | accepted |
| [003](003-waku-as-rendezvous-not-control-plane.md) | Waku as rendezvous, not a live control plane | accepted |
| [004](004-public-fleet-not-own-cluster.md) | The public logos.dev fleet, not our own cluster | accepted |
| [005](005-derived-addressing.md) | Overlay addresses derived from keys, no IPAM | accepted |
| [006](006-rotating-rendezvous-topics.md) | Rotating rendezvous topics on a stable shard | accepted |
| [007](007-separate-device-and-wireguard-keys.md) | Separate device and WireGuard keys | accepted |
| [008](008-bearer-network-key-for-v1.md) | A bearer network key for v1 | accepted, temporary |
| [009](009-probe-before-setting-endpoint.md) | Probe candidates before setting a WireGuard endpoint | accepted |
| [010](010-ship-a-container-not-a-binary.md) | Ship a container image, not a binary | accepted |
| [011](011-no-mixnet.md) | No mixnet in the data path | accepted |
| [012](012-relay-hosting.md) | Who runs the relay (you may not need a VPS) | accepted |
| [013](013-name-resolution.md) | Name resolution: hosts file now, DNS server next | accepted |
| [014](014-relay-discovery-via-announce.md) | Relay discovery: a flag on the announce, not a separate message | accepted |
| [015](015-multiple-meshes-one-daemon.md) | Multiple meshes in one daemon | accepted |
| [016](016-android-reuses-the-go-core.md) | Android reuses the Go core | accepted |
| [017](017-invite-tokens.md) | Invite tokens | accepted, built |
| [018](018-credentials-instead-of-a-shared-key.md) | Credentials instead of a shared key | accepted, mostly built |
| [019](019-service-addresses.md) | An address per service | proposed; the name router is built |
| [020](020-membership-is-a-seam.md) | Membership is a seam | accepted |
| [021](021-synthetic-ipv4.md) | A synthetic IPv4 address per peer | accepted; translator built |
| [022](022-keycard-for-the-admin-key.md) | A Keycard for the admin key | proposed; seam built, card blocked on a key-type decision |
| [023](023-announcing-services.md) | Announcing services | accepted; built |
| [024](024-ask-the-router.md) | Ask the router for a way in | accepted; built and proven |
| [025](025-control-from-a-desktop-app.md) | Control from a desktop app | accepted; settings built, admission deliberately not |
| [026](026-announce-what-is-bound.md) | Announce what is bound to the mesh address | accepted; built, off by default |
| [027](027-punching-through-the-relay.md) | Punch through the relay we already have | proposed; every part exists except the coordination |
| [028](028-when-the-fleet-turns-on-rln.md) | When the fleet turns on RLN | proposed; nothing to build yet, two questions to ask |
| [029](029-disco-proves-the-device.md) | Disco proves the device, not just the mesh | accepted; the first half of Phase 4 |
| [030](030-tailscale-shaped-not-tor-shaped.md) | Tailscale-shaped, not Tor-shaped | accepted; scopes four design notes |
| [031](031-bootstrap-from-the-mesh-itself.md) | Bootstrap from the mesh itself | proposed; after five of six fleet entry nodes refused |
