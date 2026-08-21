# Running a relay on your own connection

Offering some home bandwidth so that other people's phones can reach their own
laptops. You will not be able to read any of it.

## First: can your connection do this at all?

A relay has to be **dialable from outside**, which is the one requirement no
amount of configuration replaces.

- You need a public IP and a forwarded UDP port.
- If your connection is behind **carrier-grade NAT**, you cannot run a relay on
  it. Most mobile connections are, and a growing number of fixed ones. The
  symptom is a public IP that does not match what your router thinks it has.

Check from somewhere else — a phone on mobile data, a VPS — rather than from
inside your own network, where it will appear to work regardless:

    shrooms-relay -probe <your public address>:51820

## Then

    docker compose -f deploy/home/docker-compose.yml up -d

**Set the bandwidth ceiling first.** It is the only setting that matters and it
defaults to unlimited, which on a home line means a relay that can make
everything else in the house feel broken. A fraction of your upload, not all of
it.

## What you are agreeing to

**You cannot read any of it.** The traffic is WireGuard, encrypted between two
devices whose keys you do not have. That is arithmetic, not a promise about your
own good behaviour.

**It is not an open proxy.** A relay forwards only between two devices that have
both registered *and* answered a challenge proving they receive where they
claim. It cannot be pointed at a third party, and it is one packet in, one packet
out — no amplification. What you are offering is bandwidth, not a weapon.

**You will see some metadata**: opaque per-relay tags, the addresses they connect
from, which pairs exchange packets, how much and when. The tags are derived from
each mesh's own key, so they mean nothing to anybody else and two operators
comparing notes see unrelated numbers. It is still a traffic pattern, and if you
would rather not have it, do not run a relay.

**Your address becomes known** to whoever you give it to, and to anybody they
pass it on to. Use a token if that matters.

## Why this is not part of the daemon

Because "blind" should be structural rather than promised. A relay that runs in
its own process, holding no network key, no credential and no roster, cannot
read your mesh — and you can verify that by looking at what it is. One that
shares a process with all of those can only be trusted not to.

It also means a relay for strangers cannot take your own connectivity down with
it, and can be throttled, firewalled or stopped on its own.
