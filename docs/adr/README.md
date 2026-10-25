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
| [022](022-keycard-for-the-admin-key.md) | A Keycard for the admin key | accepted; built and proven against a physical card |
| [023](023-announcing-services.md) | Announcing services | accepted; built |
| [024](024-ask-the-router.md) | Ask the router for a way in | accepted; built and proven |
| [025](025-control-from-a-desktop-app.md) | Control from a desktop app | accepted; settings, mesh on/off, joining and leaving built |
| [026](026-announce-what-is-bound.md) | Announce what is bound to the mesh address | accepted; built, off by default |
| [027](027-punching-through-the-relay.md) | Punch through the relay we already have | proposed; every part exists except the coordination |
| [028](028-when-the-fleet-turns-on-rln.md) | When the fleet turns on RLN | proposed; nothing to build yet, two questions to ask |
| [029](029-disco-proves-the-device.md) | Disco proves the device, not just the mesh | accepted; the first half of Phase 4 |
| [030](030-tailscale-shaped-not-tor-shaped.md) | Tailscale-shaped, not Tor-shaped | accepted; scopes four design notes |
| [031](031-bootstrap-from-the-mesh-itself.md) | Bootstrap from the mesh itself | proposed; after five of six fleet entry nodes refused |
| [032](032-a-suffix-that-cannot-be-taken-away.md) | A suffix that cannot be taken away | accepted; `.mesh` was never reserved and ICANN's 2026 round has closed; the CLI's hosts file and status follow the daemon's suffix (amended 2026-10-13) |
| [033](033-the-card-is-the-admin-not-the-uid.md) | The card is the admin, not the uid | accepted, built; tiers by what protects an operation |
| [034](034-the-admin-names-the-blind-relays.md) | The admin names the blind relays | accepted; built |
| [035](035-an-edge-port-is-nobodys-contract.md) | An Edge node's port is nobody's contract | accepted; built — a tethering phone stranded the old one |
| [036](036-agents-grow-out-of-the-mesh.md) | Agents grow out of the mesh | accepted; built — Shrooms Agents, a separate app over the mesh; voice by ADR-053, harnesses by ADR-054 |
| [037](037-agents-in-cages.md) | Agents in cages, and off the machine | accepted; cages on the agent's machine built 2026-10-08 (rootless podman, a container per session); krun, Akash and spawning not yet |
| [038](038-usage-is-counted-by-the-mesh-address.md) | Usage is counted by the mesh address | accepted; built — who asked, where it ran, which model |
| [039](039-the-phone-tells-the-core-what-it-cannot-see.md) | On a phone, the app tells the core what it cannot see | accepted; built — its addresses, and when its node is deaf |
| [040](040-voices-come-from-the-system.md) | Read-aloud uses the system's voice, set up rather than bundled | accepted; built |
| [041](041-agents-talk-a2a-over-the-mesh.md) | Agents talk A2A, and the mesh is the authentication | accepted; built — every session an A2A agent, its tools in every session; "a task is a turn" amended by 042 |
| [042](042-a-task-is-open-until-the-worker-finishes-it.md) | A task is open until the worker finishes it | accepted; built — supervised by a watchdog, queued, acknowledged |
| [043](043-peers-are-probed-on-evidence-not-only-announces.md) | Peers are probed on evidence, not only on announces | accepted; built — remembered, answering, after a move, or probing us; roaming without the bus |
| [044](044-cages-reach-agents-through-their-own.md) | Cages reach agents only through their own, which says so | accepted; built — agent port closed in cages, a socket to their own agent, receivers choose (accept_caged); no shrooms control socket in a cage (amended 2026-10-09) |
| [045](045-sealed-cages-for-code-nobody-vouches-for.md) | Sealed cages, for code nobody vouches for | accepted; built — internet only (nothing local, DNS included: amended 2026-10-13), its own token, asks nothing, results in an outbox; the boundary a test that can fail (amended 2026-10-13) |
| [046](046-metrics-on-the-mesh-addresses.md) | Metrics on the mesh addresses | accepted; built — tunnels, delivery and interfaces per node; Prometheus + Grafana via deploy/metrics |
| [047](047-the-tasks-board-is-read-from-the-agents.md) | The tasks board lives in the agents' own view, read from the agents | accepted; built 2026-10-10 — links carry their tasks, cards carry their load, a panel grouped as a person needs it, ACK to the worker's agent |
| [048](048-files-between-agents.md) | Files between agents | accepted; built — push into per-sender folders, allow list empty by default, sealed cages receive only |
| [049](049-sessions-per-mesh.md) | Sessions per mesh | accepted; built — a session limited to some of its machine's meshes is absent from the others; an unnamed caller is no one |
| [050](050-basecamp-at-parity-with-the-phone.md) | Basecamp at parity with the phone | accepted; built — services per mesh, joins off the view's thread, a first join by invite open to the socket group, blind relays with the token never read back; Keycard invites through keycard-basecamp (a real-card run still owed) |
| [051](051-a-card-lets-the-socket-group-renew-and-revoke.md) | A card lets the socket group renew and revoke | accepted; built — drafts the card signs, finished and verified by the daemon; card-only meshes only; members carry sealing keys |
| [052](052-backups-on-our-own-storage-nodes.md) | Backups on our own Logos Storage nodes, encrypted to a Keycard | proposed — VPS, proteus and scribe on the office mesh, twice a day, age to a key the card derives; public storage with dispersal when it exists |
| [053](053-voice-notes-by-parakeet-in-pieces.md) | Voice notes by Parakeet, a long one in pieces at its pauses | accepted; built — Parakeet v3 at 6-8× real time, no language to pick; over 90 s cut at pauses into minute pieces run at once (406 s in 40 s, not 99 s) |
| [054](054-harnesses-speak-claude-codes-events.md) | Harnesses are pluggable, and speak Claude Code's events | accepted; built — pi the second, by its RPC mode and its own mcp.json; pinned at 1.1.0 in cages |
| [055](055-releases-go-through-the-publisher.md) | Releases go through the publisher, and public ones wait for the owner | accepted; in use — LAN repos first, public ones on a code from the owner's phone, the publisher never signs, Basecamp packages amd64 + arm64 |
| [056](056-agents-without-a-mesh.md) | Agents without a mesh, and Shrooms as the upgrade | accepted; built — the agent always on a socket of its user's, alone on it with no daemon; meshes taken up when they appear, no restart; Basecamp finds it at `local`; the installer no longer needs shrooms |
| [057](057-one-folder-every-cage-sees.md) | A folder caged sessions share | accepted; built — a cage option: a podman volume at `/shared`, read-write, one per set of meshes, never in a sealed cage; a volume defined as a bind is refused |
| [058](058-the-core-tells-the-view.md) | The core tells the Agents view what changed | accepted; built — `agentChanged` events from shrooms_core 0.26, a 3 s safety-net poll, a stream grows its row only, no infinite animations; ~0.8 core calls/s with a session open, from ~8 |
| [059](059-cages-reach-models-through-the-agent.md) | Cages reach their models through the agent, and hold no keys | accepted; built — the agent forwards `/llm/<upstream>/` on the cage's socket and adds the credentials; an in-cage forwarder with no secret; harnesses plug in by environment and files (Claude Code in sealed cages, pi in caged ones) |
| [060](060-caged-is-your-agent-in-its-folder.md) | Caged is your agent, kept in its folder | accepted; built — a cage's harness home is private, with the owner's settings read-only and only its own conversations read-write; one sentence each for not caged, caged and sealed |
| [061](061-opencode-is-a-harness-over-acp.md) | OpenCode is a harness, over its ACP server | accepted; built — `opencode acp`, streamed turns, the mesh's tools over MCP; no auto-approve (it never asks — measured), no takeover yet; the codec may write while decoding, which is what ACP needs |
| [062](062-a-harness-is-whole-or-not-offered.md) | A harness comes with everything a session has, cages included | accepted; built — `Cageable` is the interface, Claude Code and pi moved behind it unchanged, and OpenCode implements it, `CageEnv` and `Gatewayed`; a caged OpenCode session gets a data directory of its own, since its conversations are one database for every project |
| [063](063-what-each-agent-is-for.md) | What each agent is for: skills and responsibilities, set by the owner | accepted; step 1 being built — A2A skills and a responsibilities extension on each session's card, set per session by the owner, shown by list_agents; step 2 (replies continue tasks, results close once read, evidence it ran) not built |
| [064](064-an-agents-schedule-is-its-own-scala-calendar.md) | An agent's schedule is its own Scala calendar, co-owned with the owner | accepted; being built — a calendar per session, the owner and that session its only editors; routines in custom fields, checks run without the model and wake it only on a change; runs written back; the agent signs for the session and refuses any other calendar; a change notifies the owner |
| [066](066-a-name-is-held-by-its-first-claimant.md) | A device name is held by the key that claimed it first | accepted; built — online or not, until forgotten or revoked; a later claimant answers to `name-xxxx`; on a mesh with an admin the name is the credential's; without one, names stay first come, first served (audit F-18) |
| [067](067-a-core-node-relays-its-own-shard.md) | A Core node relays its own mesh's shard, not the whole network | accepted; being built — `shards` set to the mesh's shard for Core, `relay_all_shards` to relay them all; the pinned library patched (upstream #4513) until the pin moves |
| [068](068-a-member-can-speak-for-a-lan-host.md) | A member can speak for a named LAN host, never for a LAN | accepted; built — one LAN device at a time behind a gateway trusted for its LAN, `<host>.lan.<gateway>.<mesh>`, a derived address (the LAN address on no wire), a separate admin-signed grant members verify (`shrooms admin lan`), a per-port TCP proxy in a userspace stack; sealed cages can't reach hosts, normal cages can (ADR-060); services now forward to this machine only; amended after the implementation audit: every node's kernel takes only packets addressed to it, a withdrawal closes open connections, a stale serial is refused, and a member may alias a LAN address to a host (`lan_aliases`); after the re-check, private host addresses only, `--name` needs a signed name, a gateway stops with its own credential |
| [069](069-the-task-list-has-a-window.md) | The task list takes `history=0`/`since`/`limit`, and a bad one is a 400 | proposed — measured 589,776 B per poll cycle on the hub; `history=0` is 55.3% less on the real reviewer payload |
| [070](070-the-owner-allows-each-cage.md) | The owner allows tasks from each cage, not from all of them or none | accepted; being built — `accept_caged_from` per session (MACHINE/SESSION or MACHINE/*); a task from any other caged agent is held in A2A auth-required as a request, allowed (standing rule, releases its tasks) or declined (rejected, asker told); amends ADR-044 |
| [071](071-the-delivery-node-resolves-like-the-machine.md) | The delivery node resolves names the way the machine does | accepted; built — `name_servers` handed to the library as `dnsAddrsNameServers`; unset, what `/etc/resolv.conf` names; a VPN such as Mullvad is a network the machine is on, run over rather than around; settles [dns-the-library-insists-on.md](../dns-the-library-insists-on.md) |
