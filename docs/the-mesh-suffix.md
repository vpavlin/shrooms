# `.mesh`, `.shrooms`, or something that cannot break

Vaclav, 2026-08-23: *"should we switch to .shrooms instead of .mesh? or is mesh
the standard?"*

**`.mesh` is not a standard.** It is not reserved by IANA, not in RFC 6761's
special-use registry, and not designated for private use by anybody. Neither is
`.shrooms`. Both are squatting on the public namespace, and switching between
them changes nothing about the risk — it only changes which string we are
squatting on.

## The risk is real, and right now nobody can measure it

ICANN's second new gTLD round — the first since 2012 — opened on 30 April 2026
and **closed on 12 August 2026**, eleven days before this note. More than 1,600
applications were submitted.

The list is not public yet. Reveal Day is expected around **October 2026**,
after the administrative checks. So whether somebody applied for `.mesh` is now
a settled fact that nobody outside ICANN can look up for another two months.

If it was applied for and is delegated, every `.mesh` name on this network
becomes a name that also exists in the public DNS. What breaks is not
theoretical: a resolver misconfiguration, a device that falls back to its DHCP
resolver, a browser that decides a bare name is a search term — each becomes a
query leaking mesh device names to whoever runs the TLD, and a possible wrong
answer where there used to be no answer at all.

`.shrooms` is a less likely application than `.mesh`, which is a generic word
somebody plausibly wants. But "less likely" is not "cannot", and the whole
appeal of switching would be to stop worrying about it.

## What cannot break

**`.internal`.** ICANN's Board resolved in July 2024 that it will **never be
delegated in the root zone**, precisely so that private networks have a string
that cannot collide. It implements SSAC's SAC113 recommendation from 2020. That
is the modern, correct answer to this question, and it did not exist when most
projects picked their suffix.

**`.home.arpa`** (RFC 8375) is also safe, and is specifically scoped to home
networks by HNCP. Fine, and slightly awkward to type.

**A domain we own** — what Tailscale does with `.ts.net`. Zero collision risk
because it is a real registered name. It also puts the operator's identity into
every hostname, and it means the namespace depends on a renewal.

## The catch with `.internal`, and the way round it

`.internal` is *shared* private space. A corporate network may already be using
it, and a resolver that claims the whole of `.internal` would fight with that —
the resolver here is authoritative for one suffix and forwards everything else,
so claiming too much is exactly the "a VPN that quietly becomes the system
resolver" failure `internal/dns` is written to refuse.

So claim a label under it, not the top of it. Two shapes:

    vps.home.internal        one label per mesh — reads best, claims least
    vps.home.shrooms.internal  everything under one label — one suffix to claim

The first is what `home.arpa` users do and reads exactly like today's
`vps.home.mesh`. It costs the resolver having to be authoritative for one suffix
per mesh instead of one overall, which is a small change to something already
per-mesh.

## Migration is cheap here, and gets more expensive later

`hosts_suffix` is already configurable, so this is a default change rather than
a rewrite. The resolver can answer for the old suffix and the new one at once
during a transition, which costs one comparison — so nobody's ssh config breaks
on the day it changes.

The reason to decide soon rather than later is that every day adds another
`.mesh` name in somebody's ssh config, browser bookmark, and muscle memory. This
is the cheapest this decision will ever be, and October is when we find out
whether it was urgent.

## Recommendation

**Not `.shrooms`.** It fixes nothing that `.mesh` gets wrong — still
unreserved, still delegatable — and it ties the namespace to the project's name,
so a rename or a fork inherits a suffix that no longer means anything.

**Move to `<mesh>.internal`,** and support `.mesh` alongside it for as long as
it takes. It is the one option that cannot be taken away, it reads the same as
today, and the machinery to do it without breaking anybody already exists.

Worth doing before October rather than after, because if `.mesh` does turn up on
Reveal Day this stops being a tidiness question.
