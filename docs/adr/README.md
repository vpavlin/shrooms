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
