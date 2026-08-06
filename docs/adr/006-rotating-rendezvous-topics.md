# 006. Rotating rendezvous topics on a stable shard

**Status:** accepted

## Context

On a public bus, a static content topic is a permanent label. Anyone who learns
it can watch the mesh forever, and Waku's Store would let them query history
retroactively.

## Decision

Derive the content topic from the network key and a time epoch:

```
tag          = HMAC-SHA256(K_topic, "mesh/v1/rendezvous" || be64(epoch))[0:16]
contentTopic = "/<app>/<ver>/" || base32(tag) || "/proto"
```

Rotate hourly; accept `epoch-1`, `epoch`, `epoch+1` for clock skew.

## Why

Waku autosharding computes `shard = sha256(application ‖ version) mod
numShards` — hashing **only** the application and version fields, not the whole
content topic. So rotating the `{name}` field changes the topic while leaving
the shard, and therefore the gossipsub mesh, fixed.

That is what makes rotation free. Had we rotated the *pubsub* topic instead,
every rotation would emit visible SUBSCRIBE/UNSUBSCRIBE churn to our neighbours
and drop us into a fresh mesh with an anonymity set of one.

**Verified, not assumed.** Spike S3 published to six consecutive epoch topics
against a live node; all six routed to `/waku/2/rs/2/3`, matching the locally
computed value. This mattered because
[nwaku#2538](https://github.com/waku-org/nwaku/issues/2538) reports autosharding
resolving content topics to the wrong shard.

The application and version fields are deliberately generic. A distinctive
application string would be a global cleartext label identifying the mesh on
every message, even though `{name}` is opaque.

## Consequences

- Clock skew beyond the three-epoch window partitions the mesh. One hour with a
  ±1 window is very forgiving; below ~10 minutes skew becomes the binding
  constraint.
- Changing `Application` or `Version` moves every topic to a different shard, so
  they are effectively frozen.
