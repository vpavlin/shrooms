# Running a node in a container

For trying something without touching the machine you are on: a second, separate
peer on your own laptop, with its own identity, its own address and its own
config. Used for the livestream demo, and for testing relays — a container
behind Docker's bridge is genuinely undialable from outside, which is the
condition a relay exists to solve.

## The command

```
docker rm -f shrooms-demo 2>/dev/null
mkdir -p /tmp/shrooms-demo/etc /tmp/shrooms-demo/var

docker run -d --name shrooms-demo \
  --cap-add NET_ADMIN --device /dev/net/tun \
  -v /tmp/shrooms-demo/etc:/etc/shrooms \
  -v /tmp/shrooms-demo/var:/var/lib/shrooms \
  ghcr.io/vpavlin/shrooms:latest
```

Build the image first with `make image`.

`NET_ADMIN` and `/dev/net/tun` are all it needs — not `--privileged`. The two
volumes keep config and identity on the host, so the container is disposable
while its device identity and announce sequence number survive being recreated.

**Bridge networking on purpose**, which is the opposite of what
`docker/compose-node.yml` does. A production node wants `--network host`, so it
binds the machine's real address and hole punching is not fighting a layer of
NAT that does not exist in reality. A *demo* node wants the bridge: it gets its
own address, is a genuinely separate peer from the laptop hosting it, and is
unreachable from outside — which is the whole point when testing a relay.

## Joining it to a mesh

```
# on a machine already on the mesh
shrooms invite

# then, into the container
docker exec shrooms-demo shrooms join <TOKEN>
docker exec shrooms-demo shrooms status
```

## Pointing it at a blind relay

Edit `/tmp/shrooms-demo/etc/config.toml` on the host and restart the container:

```toml
relay_blind = ["203.0.113.10:31760"]
relay_token = "only if the operator asks for one"
```

```
docker restart shrooms-demo
docker exec shrooms-demo shrooms status
```

## Watching it

```
docker logs -f shrooms-demo
docker exec shrooms-demo shrooms status
docker exec shrooms-demo shrooms paths
docker exec shrooms-demo shrooms status --ipv4
```

## Tearing it down

```
docker rm -f shrooms-demo && rm -rf /tmp/shrooms-demo
```

Removing the state directory is what makes it a *new* device next time. Keeping
it means the same identity comes back, which is usually what you want and
occasionally not: a peer that has been revoked, or whose credential has expired,
stays that way until its identity is discarded.

## Testing a relay with it

The useful property is that neither end can be dialled:

1. A phone on mobile data, behind carrier-grade NAT.
2. This container, behind Docker's bridge.
3. Both given the same `relay_blind`.

Nothing about that pair can hole-punch, so if they reach each other at all, they
did it through the relay. Confirm on the relay itself — `carried` and `packets`
climb in its logs — and with `shrooms paths` at either end.
