# 045. Sealed cages, for code nobody vouches for

**Status:** accepted 2026-10-09, built the same day — a third level beside
"uncaged" and "caged" (ADR-037, ADR-044); in use in
[docs/agents/cages.md](../agents/cages.md)

## Context

The owner reviews code from outside: a contributor's pull request, a whole
application a community member built. Such code may be hostile, and an agent
working on it reads it, runs it, installs what it asks for. A cage (ADR-037)
contained the machine but not what the session could reach. Tried for real
on 2026-10-09, an ordinary cage still:

- reached the LAN: the router's admin page and jimmy-crib's file server
  answered from inside;
- had the owner's Claude Code login read-write — the token, and the
  transcripts of every conversation in every project;
- was handed the model and provider keys in the agent's environment;
- could ask the owner's other agents for work through its proxy, so a
  prompt injection in the reviewed code could travel.

## Decision

**A sealed cage**: chosen per session ("sealed" beside "in a cage").

- **The internet and nothing local.** An nftables rule in the cage's network
  namespace, set from outside on every start as for the agent port: private,
  mesh (IPv6 and its IPv4), link-local, multicast and loopback-range
  addresses refused; DNS, the cage's own loopback (an app it runs to test)
  and the ICMPv6 IPv6 needs let through.
- **A credential of its own.** Claude Code on the machine's sealed token
  (`claude setup-token`, revocable from the owner's account), with its
  settings and transcripts in a directory of its own. None of `~/.claude`,
  none of the agent's environment. Claude Code only: pi would need the
  owner's model keys. GitHub and nix are not offered.
- **No user namespaces.** Asked to look for ways out of its own sealed cage,
  the reviewer found the boundary held — root inside is an unprivileged user
  outside, no dangerous capabilities, no runtime sockets, no cgroup v1
  release agent, no metadata endpoint — but that it could create a user
  namespace and hold every capability in it: the door nearly every recent
  container escape (overlayfs, nf_tables, io_uring) has gone through. A
  sealed cage runs under podman's default seccomp profile with `clone` and
  `unshare` allowed only without `CLONE_NEWUSER`, and `clone3` answering
  ENOSYS so that callers fall back to `clone`, where the flag can be seen.
  Processes, threads and Claude Code are unaffected; tools that need user
  namespaces (`bwrap`) do not work there, which ordinary cages keep.
- **Answers, asks nothing.** Its socket to its agent finishes its own tasks
  and does nothing else.
- **Results through an outbox**: `~/shrooms-outbox/<session>` on the machine,
  mounted read-write at `/outbox`, with a note that what is in it is
  untrusted text. Uploading somewhere on the internet works too.
- The project, and the files sent to that session only, as before.

## Consequences

- A hostile review can waste its token's quota and reach the internet. It
  cannot touch the LAN, the mesh, the owner's login or other conversations,
  nor instruct another agent.
- Whoever reads the outbox — the owner, or an agent — must treat it as
  untrusted input: a review of malicious code can carry instructions aimed at
  its reader. The outbox says so.
- A session moved into a sealed cage keeps its conversation: that one
  transcript is copied into the cage's own directory when the cage is made,
  and no other (without it Claude Code stopped with "No conversation found",
  2026-10-09). So the code under review can read that conversation; start a
  new session for a review if it holds anything that should not be read.
  From then on the cage's copy is the conversation, and history the agent
  reads from `~/.claude` stops at the move; its own events are complete.
- The machine needs a token: until one is saved, the apps say how.
