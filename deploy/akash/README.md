**It works without a lease — on the right provider.** Confirmed 2026-08-21 on
digitalfrontier.so in `eu-se-1`, which forwarded UDP on an assigned node port:

    $ shrooms-relay -probe provider.h6i-dedicated.eu-se-1.digitalfrontier.so:32684
      first   device registered in 187ms (challenge answered)
      second  device registered in 32ms (challenge answered)
      packet relayed in 33ms

    OK — forwards, and cannot be pointed at an address that does not answer

That is a real blind relay on ephemeral compute, no IP lease, no volume, at
roughly a tenth the price of the leased variant.

**But it is provider-dependent, and two providers did not do it.** zencloud.eu
(dseq 28269647) and cpu.dal.aes.akash.pub (dseq 28269739) both ran the container
correctly and forwarded nothing. On the Dallas one this was measured rather than
guessed: sweeping the provider's node address — 209.135.147.17, which is *not*
the 209.135.147.15 the ingress hostname resolves to — found TCP node ports wide
open and the same range on UDP answering nothing. So Kubernetes creates the UDP
mapping, as the provider code says it does, and that provider's network does not
carry it.

**So: try without a lease first, and be ready to move providers.** The probe
settles it in one command, and a provider that does not forward UDP costs
nothing but the time to find out.

# Running a blind relay

A blind relay forwards packets between devices that cannot reach each other
directly, for meshes it is not a member of. It cannot read what it carries —
the packets are WireGuard, encrypted between two devices whose keys it does not
have — and it never learns which mesh anything belongs to.

Read [docs/blind-relays.md](../../docs/blind-relays.md) before running one for
strangers. In particular: **this is an experiment, offered with no guarantees of
any kind.** It has not been audited, it may lose your traffic, and it may be
withdrawn at any time. It is shared so people can poke at it.

## What the operator can and cannot see

**Cannot:** message contents, any network key, who is on a mesh, device names,
or anything from a control plane.

**Can:** opaque per-relay tags, the IP addresses those tags connect from, which
pairs of tags exchange packets, how much, and when. That is a traffic-analysis
surface. It is smaller than it sounds — the tags are meaningless off this
particular relay, so two operators comparing notes see unrelated numbers — and
it is not nothing.

## Anywhere a container runs

    docker run -d --name shrooms-relay \
      -p 51820:51820/udp \
      -e SHROOMS_RELAY_BYTES_PER_SECOND=12500000 \
      ghcr.io/vpavlin/shrooms-relay:latest

The image is about 2.5 MB and runs on scratch as an unprivileged user. It keeps
no state, needs no volume, and holds no identity, so redeploying it costs one
refresh interval of relayed traffic and nothing else.

Build it yourself with:

    docker build -f docker/relay.Dockerfile -t shrooms-relay .

Check any relay, yours or somebody else's, with `shrooms-relay -probe` — see
below.

## Deployments are paid in ACT, not AKT

Worth knowing before the first attempt, because the failure is opaque. Creating
a deployment with an AKT deposit is refused by the chain outright:

    Deposit invalid ... with gas used: '35786': unknown request

That is not a configuration mistake. `x/deployment/handler/server.go` rejects it
by design:

```go
// AKT deposits are only allowed via AccountDeposit (existing deployment)
if msg.Deposit.Amount.Denom == sdkutil.DenomUakt {
    return nil, v1.ErrInvalidDeposit
}
```

So AKT can top up a deployment that already exists, and cannot create one.
Deposits and pricing are both in **`uact`**, which you mint from AKT:

    provider-services tx bme mint-act <amount>uakt --from <key> ...

Mint **at least 10 ACT** — smaller amounts have been reported not to become
spendable. There is an open issue where minted ACT does not appear in
`query bank balances` at all
([akash-network/support#445](https://github.com/akash-network/support/issues/445)),
so if `deployment create` then fails with `insufficient balance` rather than
`Deposit invalid`, that is a known problem with the chain rather than with
anything here.

The `pricing.denom` in both descriptors is `uact` for the same reason.

## Where to deploy from

**https://air.akash.network/** — Console Air, the console that takes **AKT** as
payment. The one the docs send you to, `console.akash.network`, is not the same
thing, which is easy to lose track of when you only do this occasionally.

## Publish the image first

The descriptors reference `ghcr.io/vpavlin/shrooms-relay:latest`, which does not
exist until somebody pushes it. Akash providers are amd64, so build for that
explicitly:

    docker build --platform linux/amd64 \
      -f docker/relay.Dockerfile -t ghcr.io/vpavlin/shrooms-relay:latest .
    docker push ghcr.io/vpavlin/shrooms-relay:latest

**A newly pushed ghcr package is private, and the REST API cannot change that** —
it is a web-UI setting. A provider pulls with no credentials of yours, so until
it is flipped the deployment fails on the pull:

    https://github.com/users/vpavlin/packages/container/shrooms-relay/settings
    → Danger Zone → Change visibility → Public

Check it from outside rather than trusting the setting:

    TOKEN=$(curl -s "https://ghcr.io/token?scope=repository:vpavlin/shrooms-relay:pull&service=ghcr.io" \
      | python3 -c "import json,sys;print(json.load(sys.stdin)['token'])")
    curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer $TOKEN" \
      https://ghcr.io/v2/vpavlin/shrooms-relay/manifests/latest

`200` means a provider can pull it. `403` means it is still private.

## On Akash

Two descriptors here. **Start with `relay-noip.yaml`.**

### Choosing a provider that can do it

Providers advertise IP-lease support under **two different attribute keys**, and
SDL attributes are AND-matched — so filtering on either one silently excludes
everybody using the other. On mainnet, 2026-08-21: 8 providers advertise
`ip-lease: true`, and 4 advertise `feat-endpoint-ip: true`.

`relay.yaml` therefore filters on neither. Choose from the bids instead, and
check a candidate before accepting:

    curl -s https://akash-api.polkachu.com/akash/provider/v1beta4/providers/<addr> \
      | python3 -m json.tool | grep -i "endpoint-ip\|ip-lease\|host_uri"

Everything about a deployment is public, which is useful when the console is
unhelpful and the CLI wants a key you would rather not export. Bids, leases and
provider records all read without authentication:

    API=https://akash-api.polkachu.com
    curl -s "$API/akash/market/v1beta5/leases/list?filters.owner=<addr>&filters.state=active"
    curl -s "$API/akash/market/v1beta5/bids/list?filters.owner=<addr>&filters.dseq=<dseq>"

`relay.yaml` takes an IP lease, which is the expensive part — often more than
the compute under it. What a lease actually buys is the ability to *choose* a
port. Akash's own announcement puts the limitation it removed plainly:

> some services (like a VPN, for example) must use standard ports in the 0-1024
> range, which isn't possible unless you have a dedicated IP

Before leases you were still exposed, on a port the provider picked; you just
had no say in which. **A relay does not need a say.** It is reached at whatever
address and port its users are configured with, and that is an arbitrary value
either way. So `relay-noip.yaml` exposes UDP with no lease, the provider assigns
a high port, and `provider lease-status` tells you which.

The real cost of going without is that the assigned port can change when the
deployment is recreated, so the address is not stable across redeploys. For a
relay handed out alongside a token, that is a line in a message you were sending
anyway.

**Tried on two providers: the no-lease path does not work, and now we know why.**
Deployed `relay-noip.yaml` on zencloud.eu (dseq 28269647) and on
cpu.dal.aes.akash.pub (dseq 28269739), 2026-08-21. Both times the container
started and logged its whole configuration correctly, so the image and the
descriptor are sound. Neither console showed a forwarded port, and nothing
answered on UDP.

The first sweep was aimed at the wrong host and proved nothing — it used the
address the ingress hostname resolves to, and a NodePort lives on the node. The
two are genuinely different machines:

    ingress    209.135.147.15
    provider   209.135.147.17

Sweeping the *node* settles it. TCP NodePorts there are wide open — twenty-odd
ports answering, other tenants' services — while the same range on UDP answers
nothing at all. So the address is right, NodePorts do reach the internet, and
**this provider forwards TCP and not UDP.**

That is consistent with the provider code, which does build UDP NodePorts
(`cluster/kube/builder/service.go` maps `manitypes.UDP` to `corev1.ProtocolUDP`
for any global non-ingress expose). Kubernetes creates the mapping; the
provider's network does not carry it. Nothing in an SDL can fix that.

### The console URL is not the relay's address

A provider hands out an `https://<hash>.ingress.<provider>/` hostname for every
deployment, and the console shows it as *the* URI. **It is not the relay.** That
hostname is an HTTP ingress, and a relay speaks UDP, so fetching it returns
`502` — which is the expected result rather than a fault.

Akash only routes a service through ingress when it is TCP on port 80:

```go
func (s *ServiceExpose) IsIngress() bool {
	return s.Proto == TCP && s.Global && uint32(80) == s.GetExternalPort()
}
```

A UDP service gets a **forwarded port** instead: the provider's host and an
assigned high port, listed under `forwarded_ports` in the lease status.

    provider-services lease-status --dseq <DSEQ> --from <key> --provider <provider> \
      | python3 -m json.tool | grep -A 10 forwarded_ports

Look for `externalPort` together with `host`, and note the `proto` says `UDP`.
That pair is the relay's address, and it is what goes in `relay_addr` and what
you hand to anybody using it.

### Checking it works

    shrooms-relay -probe <host>:<port>

This is a client, not an inspection: it stands up two throwaway devices,
registers both the way a real one would, and relays a packet between them. A
pass means the whole path works — NAT, load balancer, forwarded port and all —
rather than that something is listening.

    probing relay.example.com:31234 (no token)
      first   device registered in 41ms (challenge answered)
      second  device registered in 39ms (challenge answered)
      packet relayed in 42ms

    OK — relay.example.com:31234 forwards, and cannot be pointed at an address
    that does not answer

A failure names the likely causes, since from outside they look identical:
unreachable, port not forwarded, or the wrong token.

Add `-token <t>` if the relay wants one.

## Settings

| variable | default | what it does |
|---|---|---|
| `SHROOMS_RELAY_PORT` | 51820 | UDP port |
| `SHROOMS_RELAY_TOKEN` | *(empty)* | require a token; empty means open |
| `SHROOMS_RELAY_BYTES_PER_SECOND` | 0 | total ceiling; 0 is unlimited |
| `SHROOMS_RELAY_PEER_BYTES_PER_SECOND` | 0 | per-device ceiling |
| `SHROOMS_RELAY_MAX_REGISTRATIONS` | 512 | table size |
| `SHROOMS_RELAY_MAX_PER_SOURCE` | 8 | registrations one IP may hold |

Set a bandwidth ceiling. An open relay without one is an unbounded claim on a
machine you are paying for, and the relay says so on startup rather than letting
you find out from a bill.

## Is it safe to leave open?

Open does not mean unauthenticated in the way that usually matters. The danger
with an open forwarder is **reflection** — someone points it at a third party
and it sends traffic there. A registration here installs nothing until the
registrant echoes a nonce sent to the address it gave, which an attacker
registering somebody else's address never receives. Relaying is also one packet
in, one packet out, so there is no amplification to be had.

What is left to abuse is bandwidth, which is what the ceilings are for. A token
is about choosing *who* uses it, not about safety.
