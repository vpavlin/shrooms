**It works without a lease.** Confirmed 2026-08-21 on digitalfrontier.so in
`eu-southeast`, node port only, no `endpoints:` block, ~$3.50/month:

    $ shrooms-relay -probe provider.h6i-dedicated.eu-se-1.digitalfrontier.so:30730
      first   device registered in 32ms (challenge answered)
      second  device registered in 33ms (challenge answered)
      packet relayed in 31ms

    OK — forwards, and cannot be pointed at an address that does not answer

A blind relay on ephemeral compute: no dedicated address, no volume, no state,
nothing to restore, and a 2.5 MB container that cannot read a byte of what it
carries.

The price is the provider's compute rather than anything structural. That
provider will not bid on the minimal profile at all, so this is the raised one
(0.5 CPU / 512Mi / 1Gi); providers bidding on the minimal profile came in around
a fifth of the price, and whether any of *those* forward UDP is the open
question. Each is a few minutes and fractions of a cent to find out.

**But it is provider-dependent, and two others forwarded nothing.** zencloud.eu
and cpu.dal.aes.akash.pub both ran the container correctly and forwarded no UDP.
On the Dallas one this was measured rather than guessed: sweeping the provider's
*node* address — 209.135.147.17, not the 209.135.147.15 the ingress hostname
resolves to — found TCP node ports wide open and the same range on UDP silent.
Kubernetes creates the UDP mapping, as the provider code says it does, and that
provider's network does not carry it.

**So: try without a lease, and be ready to move providers.** One probe settles
it, and a provider that will not forward UDP costs only the minutes to find out.

### When the provider you want does not bid

A relay asks for almost nothing — 0.1 CPU, 128Mi of memory — and some providers
will not bid on a deployment that small. Observed 2026-08-21: seventeen
providers bid on `relay-noip.yaml`, and digitalfrontier.so was not among them,
though it had bid on and won the *same* deployment with an IP lease attached. A
lease is separately billable, which is enough to carry a request the compute
alone does not.

The lever is the resource profile, not the price cap: the cap is a maximum you
will pay, and bids came in at well under a thousandth of it. So if a particular
provider will not bid, ask for more:

    resources:
      cpu:
        units: 0.5
      memory:
        size: 512Mi
      storage:
        size: 1Gi

That is buying bid eligibility rather than capacity — the relay does not need
any of it.

**Confirmed, and it does not automatically pay off.** Raising the profile did
bring digitalfrontier in, which settles the floor hypothesis: it bids on the
larger request and ignores the smaller one. But it bid about twice the price of
the same provider's small-request-plus-IP-lease deployment. For that provider
the lease is the cheaper way to be reachable, which is the opposite of the
assumption this whole exercise started from.

So the thing to optimise for is a provider that forwards UDP node ports **and**
has cheap compute. Those are independent properties and the second is easy to
read off the bids; the first can only be found by deploying and probing. Testing
several is cheap — each is fractions of a cent for the minutes it takes to
probe and close.

A GPU provider is a separate case and probably not worth chasing. Europlots
advertises six GPU capability keys and thirteen active leases, so it is busy
with work that pays far better than a relay.

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
