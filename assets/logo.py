#!/usr/bin/env python3
"""Generate the Shrooms mark: fruiting bodies above ground, mycelium below.

The first version put the mesh *inside the cap*, as gills. It looked good and
it said the wrong thing twice.

Mycelium is not in the mushroom. It is the network under the soil, and a
mushroom is only its fruiting body — the part that pushes up where you can see
it. Which is exactly this project's architecture: your devices are what you
look at, and the mesh is the thing connecting them that you never see. So the
mark now draws the ground: mushrooms above it, a network of hyphae below,
every stem rooted in a node of that network.

The mushrooms are also shaped like the ones the project is named after. A
psilocybe has a slender stem and a tall bell or conical cap, often with a small
point at the apex — not the wide flat saucer of a storybook toadstool. And the
bruising is real: psilocybin mushrooms stain blue where handled, which is why
the stems go violet at the base. That violet is already in the palette, where
it means "relayed", so the mark uses it for the part that touches the network.

One geometry, three outputs — a PNG with real glow, an Android VectorDrawable
for the adaptive icon, and an SVG for documents and the website. Generated
rather than drawn so the launcher icon, the package icon, the favicon and the
website cannot drift apart, which is exactly what happens when someone edits
one of four exported files.

    python3 assets/logo.py

Deterministic: fixed seeds, so regenerating produces the same mark. A logo that
reshuffles itself on every build is not a logo.
"""
import math
import os
import random
import xml.sax.saxutils as sx

from PIL import Image, ImageDraw, ImageFilter

# Adaptive-icon geometry: a 108-unit viewport whose middle 72 units are the
# safe zone. Everything meaningful stays inside that circle or the launcher
# will crop it.
VIEW = 108.0
CX = 54.0

# The soil line. High enough that the mycelium gets real room — it is the
# subject as much as the mushrooms are — and low enough that the caps are not
# crammed against the top of the safe zone.
HORIZON = 62.0

VOID = (7, 9, 11)
EARTH = (26, 20, 15)          # under the soil, barely lighter than the void
EARTH_LINE = (58, 42, 31)     # the soil line itself
# Tan, because that is what these mushrooms are, and muted because phosphor is
# the only colour in this project allowed to shout.
CAP = (198, 168, 108)
CAP_DARK = (118, 95, 55)
STEM = (226, 224, 214)        # pale, slightly warm
PHOSPHOR = (53, 240, 160)     # the living network
VIOLET = (154, 123, 255)      # bruising, and "relayed" everywhere else
BONE = (214, 221, 227)

# Three fruiting bodies: one grown, two coming up. Enough to read as "several
# devices" rather than "a mushroom", which is the point — the mesh is the
# subject and these are what it produced.
#
#   x, cap half-width, cap height, stem height, lean
SHROOMS = [
    (54.0, 13.0, 22.0, 28.0, 0.0),
    (29.0, 7.6, 13.0, 16.0, -0.16),
    (78.0, 6.4, 11.0, 12.5, 0.14),
]


def cap_outline(x0, w, h, base_y, lean, n=26):
    """A psilocybe cap: a tall bell with a small point at the apex.

    Not an ellipse. The profile is a power curve, which puts the shoulders low
    and the mass high, and the papilla — the little nipple real ones carry — is
    a narrow bump added at the top. Both are what make the silhouette read as
    this mushroom rather than as a generic one.
    """
    pts = []
    # A normalised gaussian: round over the top, falling away steeply at the
    # shoulders and meeting the rim exactly. A power curve was tried first and
    # produced straight sides — three party hats rather than three mushrooms.
    k = 2.9
    def bell(u):
        return (math.exp(-k * u * u) - math.exp(-k)) / (1.0 - math.exp(-k))

    for i in range(n + 1):
        u = -1.0 + 2.0 * i / n
        y = base_y - h * bell(u)
        # The papilla, narrow enough to read as a point and not a spike.
        y -= h * 0.07 * math.exp(-((u / 0.16) ** 2))
        pts.append((x0 + w * u + lean * (base_y - y) * 0.5, y))

    # The underside, curving up slightly to the stem so the cap sits on it
    # rather than floating above it.
    for i in range(n + 1):
        u = 1.0 - 2.0 * i / n
        y = base_y - h * 0.09 * (1.0 - u * u)
        pts.append((x0 + w * u + lean * (base_y - y) * 0.5, y))
    return pts


def stem_outline(x0, half, top_y, bot_y, lean, n=14):
    """A slender stem, wider at the base and leaning a little.

    Real ones are not straight, and a mark made of three identical vertical
    lines looks printed rather than grown.
    """
    left, right = [], []
    for i in range(n + 1):
        t = i / n
        y = top_y + (bot_y - top_y) * t
        # A gentle flare at the foot, where it enters the soil.
        wdt = half * (1.0 + 0.55 * t * t)
        # The same 0.5 factor the cap uses. They were different, and the
        # rightmost cap ended up beside its stem rather than on it.
        bend = lean * (bot_y - y) * 0.5
        left.append((x0 + bend - wdt, y))
        right.append((x0 + bend + wdt, y))
    return left + right[::-1]


def mycelium():
    """The network under the soil: nodes, hyphae, and the roots of each stem.

    Built the way the mesh actually is — nodes linked to their nearest
    neighbours, with a few longer links so it is a mesh and not a chain — and
    then every mushroom is attached to the node nearest its foot. That last
    part is the whole idea of the mark: what you see above the ground is
    growing out of what you do not.
    """
    rnd = random.Random(20260813)
    nodes = []

    # Rows rather than a uniform scatter: hyphae spread sideways, and rows give
    # the eye horizontal structure to read as "underground" instead of "stars".
    for row, (y, count) in enumerate([(70.0, 5), (78.0, 6), (86.0, 4)]):
        for i in range(count):
            t = (i + 0.5) / count
            x = 16.0 + 76.0 * t + rnd.uniform(-3.0, 3.0)
            nodes.append((x, y + rnd.uniform(-2.2, 2.2)))

    edges = []
    # Nearest-neighbour links, then a few longer ones.
    for i, (xi, yi) in enumerate(nodes):
        best = sorted(
            (j for j in range(len(nodes)) if j != i),
            key=lambda j: (nodes[j][0] - xi) ** 2 + (nodes[j][1] - yi) ** 2,
        )
        for j in best[:2]:
            if (min(i, j), max(i, j)) not in edges:
                edges.append((min(i, j), max(i, j)))
    for _ in range(4):
        a, b = rnd.randrange(len(nodes)), rnd.randrange(len(nodes))
        if a != b and (min(a, b), max(a, b)) not in edges:
            edges.append((min(a, b), max(a, b)))

    # Nearest-neighbour linking can leave a pair connected only to each other,
    # which draws as two dots and a stick off in the corner and reads as a
    # rendering fault rather than as part of the network. Join every component
    # to the rest by its closest pair.
    parent = list(range(len(nodes)))

    def find(a):
        while parent[a] != a:
            parent[a] = parent[parent[a]]
            a = parent[a]
        return a

    for a, b in edges:
        parent[find(a)] = find(b)

    while len({find(i) for i in range(len(nodes))}) > 1:
        groups = {}
        for i in range(len(nodes)):
            groups.setdefault(find(i), []).append(i)
        keys = sorted(groups)
        home, other = groups[keys[0]], [i for k in keys[1:] for i in groups[k]]
        a, b = min(((i, j) for i in home for j in other),
                   key=lambda p: (nodes[p[0]][0] - nodes[p[1]][0]) ** 2
                   + (nodes[p[0]][1] - nodes[p[1]][1]) ** 2)
        edges.append((min(a, b), max(a, b)))
        parent[find(a)] = find(b)

    # The roots: each stem's foot joins the nearest node, so the mushrooms are
    # visibly part of the network rather than standing on top of it.
    roots = []
    for x0, _w, _h, sh, lean in SHROOMS:
        foot = (x0 + lean * sh * 0.2, HORIZON)
        near = min(nodes, key=lambda n: (n[0] - foot[0]) ** 2 + (n[1] - foot[1]) ** 2)
        roots.append((foot, near))

    return nodes, edges, roots


def spores(n=6):
    """Spores drifting off the tallest cap — how a mesh gains a member."""
    rnd = random.Random(7)
    x0, w, h, sh, _lean = SHROOMS[0]
    top = HORIZON - sh - h
    return [
        (x0 + rnd.uniform(-1.4, 2.6) * w * 0.8,
         top + rnd.uniform(-9.0, 5.0),
         rnd.uniform(0.8, 1.7))
        for _ in range(n)
    ]


# At 48 pixels the full drawing turns to mush: three thin stems become three
# scratches and fourteen nodes become a green smear. So small renders get a
# simplified version of the same idea — one fruiting body, four nodes — rather
# than a second drawing that would drift from this one.
# Chunkier than the full drawing on purpose: at this size a tall thin stem
# under a narrow cap reads as an exclamation mark, not a mushroom.
SHROOMS_SMALL = [(54.0, 23.0, 25.0, 16.0, 0.0)]
NODES_SMALL = [(23.0, 72.0), (42.0, 85.0), (67.0, 83.0), (86.0, 70.0)]
EDGES_SMALL = [(0, 1), (1, 2), (2, 3), (0, 2)]


def _geometry(simple=False):
    """Everything the three renderers draw, computed once."""
    shrooms = SHROOMS_SMALL if simple else SHROOMS
    caps, stems = [], []
    for x0, w, h, sh, lean in shrooms:
        base = HORIZON - sh
        caps.append(cap_outline(x0, w, h, base, lean))
        stems.append(stem_outline(x0, max(1.15, w * (0.135 if simple else 0.115)),
                                  base - h * 0.06, HORIZON + 1.5, lean))
    if simple:
        foot = (SHROOMS_SMALL[0][0], HORIZON)
        near = min(NODES_SMALL, key=lambda n: (n[0] - foot[0]) ** 2 + (n[1] - foot[1]) ** 2)
        return caps, stems, NODES_SMALL, EDGES_SMALL, [(foot, near)]

    nodes, edges, roots = mycelium()
    return caps, stems, nodes, edges, roots


def _poly(points):
    """A polygon as SVG/VectorDrawable path data."""
    d = "M %.2f %.2f " % points[0]
    d += " ".join("L %.2f %.2f" % p for p in points[1:])
    return d + " Z"


def _lines(pairs):
    """Line segments as path data, for the hyphae."""
    return " ".join("M %.2f %.2f L %.2f %.2f" % (a[0], a[1], b[0], b[1])
                    for a, b in pairs)


# --- PNG ---------------------------------------------------------------------

SMALL = 128


def render_png(path, size, background=True):
    big = size >= SMALL
    caps, stems, nodes, edges, roots = _geometry(simple=not big)
    s = size / VIEW

    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    def P(x, y):
        return (x * s, y * s)

    def poly(points, fill):
        d.polygon([P(*p) for p in points], fill=fill)

    if background:
        d.rounded_rectangle([0, 0, size - 1, size - 1], radius=size * 0.22, fill=VOID)
        # The soil, as a wash rather than a hard block: a mark with a visible
        # rectangle in it stops being a mark.
        d.rectangle([0, HORIZON * s, size, size], fill=EARTH)

    # Hyphae first, glowing, so the mushrooms sit in front of the network.
    glow = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    gd = ImageDraw.Draw(glow)
    width = max(1, int(size * (0.022 if big else 0.045)))
    for i, j in edges:
        gd.line([P(*nodes[i]), P(*nodes[j])], fill=PHOSPHOR + (150,), width=width)
    for foot, near in roots:
        gd.line([P(*foot), P(*near)], fill=PHOSPHOR + (170,), width=width)
    glow = glow.filter(ImageFilter.GaussianBlur(size * 0.016))
    img.alpha_composite(glow)

    thin = max(2, int(size * (0.008 if big else 0.022)))
    for i, j in edges:
        d.line([P(*nodes[i]), P(*nodes[j])], fill=PHOSPHOR + (235,), width=thin)
    for foot, near in roots:
        d.line([P(*foot), P(*near)], fill=PHOSPHOR + (255,), width=thin)

    r = size * (0.014 if big else 0.045)
    for x, y in nodes:
        cx, cy = P(x, y)
        d.ellipse([cx - r, cy - r, cx + r, cy + r], fill=BONE)

    # The soil line, drawn over the hyphae so the ground reads as a surface.
    d.line([P(4, HORIZON), P(VIEW - 4, HORIZON)],
           fill=EARTH_LINE, width=max(1, int(size * 0.012)))

    # Mushrooms, tallest last so it sits in front.
    for k in ([0] if len(caps) == 1 else [1, 2, 0]):
        poly(stems[k], STEM)
        # Bruising where the stem meets the soil.
        foot = stems[k][len(stems[k]) // 2 - 2:len(stems[k]) // 2 + 2]
        if foot:
            d.line([P(*foot[0]), P(*foot[-1])], fill=VIOLET, width=max(1, int(size * 0.02)))
        poly(caps[k], CAP)
        # A darker underside, so the cap has a lip rather than being a flat
        # shape stuck on a stick.
        under = caps[k][len(caps[k]) // 2:]
        poly(under, CAP_DARK)

    if big:
        for x, y, rr in spores():
            cx, cy = P(x, y)
            rr = rr * s
            d.ellipse([cx - rr, cy - rr, cx + rr, cy + rr], fill=VIOLET + (220,))

    img.save(path)
    return path


# --- Android VectorDrawable --------------------------------------------------

def render_vector(path):
    caps, stems, nodes, edges, roots = _geometry()

    def hexa(c):
        return "#%02X%02X%02X" % c

    parts = ['<?xml version="1.0" encoding="utf-8"?>',
             "<!-- Generated by assets/logo.py. Do not edit by hand; edit the generator. -->",
             '<vector xmlns:android="http://schemas.android.com/apk/res/android"',
             '    android:width="108dp" android:height="108dp"',
             '    android:viewportWidth="108" android:viewportHeight="108">',
             "",
             "    <!-- the soil -->",
             f'    <path android:pathData="M 0 {HORIZON} L 108 {HORIZON} L 108 108 L 0 108 Z" '
             f'android:fillColor="{hexa(EARTH)}"/>',
             "",
             "    <!-- mycelium: the mesh, where mycelium actually lives -->"]

    hyphae = _lines([(nodes[i], nodes[j]) for i, j in edges] +
                    [(f, n) for f, n in roots])
    parts.append(f'    <path android:pathData="{hyphae}" android:strokeColor="{hexa(PHOSPHOR)}" '
                 f'android:strokeWidth="2.6" android:strokeAlpha="0.35" android:strokeLineCap="round"/>')
    parts.append(f'    <path android:pathData="{hyphae}" android:strokeColor="{hexa(PHOSPHOR)}" '
                 f'android:strokeWidth="1.1" android:strokeLineCap="round"/>')
    for x, y in nodes:
        parts.append(f'    <path android:pathData="M {x:.2f} {y - 1.55:.2f} '
                     f'a 1.55 1.55 0 1 0 0.01 0 Z" android:fillColor="{hexa(BONE)}"/>')

    parts.append("")
    parts.append(f'    <path android:pathData="M 4 {HORIZON} L 104 {HORIZON}" '
                 f'android:strokeColor="{hexa(EARTH_LINE)}" android:strokeWidth="1.4"/>')
    parts.append("")
    parts.append("    <!-- fruiting bodies: what the network grew -->")
    for k in (1, 2, 0):
        parts.append(f'    <path android:pathData="{_poly(stems[k])}" '
                     f'android:fillColor="{hexa(STEM)}"/>')
        parts.append(f'    <path android:pathData="{_poly(caps[k])}" '
                     f'android:fillColor="{hexa(CAP)}"/>')
        parts.append(f'    <path android:pathData="{_poly(caps[k][len(caps[k]) // 2:])}" '
                     f'android:fillColor="{hexa(CAP_DARK)}"/>')
    for x, y, r in spores():
        parts.append(f'    <path android:pathData="M {x:.2f} {y - r:.2f} '
                     f'a {r:.2f} {r:.2f} 0 1 0 0.01 0 Z" android:fillColor="{hexa(VIOLET)}"/>')
    parts.append("</vector>")

    with open(path, "w") as f:
        f.write("\n".join(parts) + "\n")
    return path


# --- SVG ---------------------------------------------------------------------

def render_svg(path):
    caps, stems, nodes, edges, roots = _geometry()

    def hexa(c):
        return "#%02X%02X%02X" % c

    hyphae = _lines([(nodes[i], nodes[j]) for i, j in edges] +
                    [(f, n) for f, n in roots])

    parts = ['<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 108 108" '
             'width="512" height="512">',
             '<defs><filter id="g" x="-50%" y="-50%" width="200%" height="200%">'
             '<feGaussianBlur stdDeviation="1.8"/></filter></defs>',
             f'<rect width="108" height="108" rx="24" fill="{hexa(VOID)}"/>',
             f'<path d="M 0 {HORIZON} L 108 {HORIZON} L 108 108 L 0 108 Z" fill="{hexa(EARTH)}"/>',
             f'<g filter="url(#g)" opacity="0.85"><path d="{sx.escape(hyphae)}" '
             f'stroke="{hexa(PHOSPHOR)}" stroke-width="2.6" fill="none" stroke-linecap="round"/></g>',
             f'<path d="{sx.escape(hyphae)}" stroke="{hexa(PHOSPHOR)}" stroke-width="1.0" '
             f'fill="none" stroke-linecap="round"/>']
    for x, y in nodes:
        parts.append(f'<circle cx="{x:.2f}" cy="{y:.2f}" r="1.55" fill="{hexa(BONE)}"/>')
    parts.append(f'<path d="M 4 {HORIZON} L 104 {HORIZON}" stroke="{hexa(EARTH_LINE)}" '
                 f'stroke-width="1.4" stroke-linecap="round"/>')
    for k in (1, 2, 0):
        parts.append(f'<path d="{sx.escape(_poly(stems[k]))}" fill="{hexa(STEM)}"/>')
        parts.append(f'<path d="{sx.escape(_poly(caps[k]))}" fill="{hexa(CAP)}"/>')
        parts.append(f'<path d="{sx.escape(_poly(caps[k][len(caps[k]) // 2:]))}" '
                     f'fill="{hexa(CAP_DARK)}"/>')
    for x, y, r in spores():
        parts.append(f'<circle cx="{x:.2f}" cy="{y:.2f}" r="{r:.2f}" fill="{hexa(VIOLET)}"/>')
    parts.append("</svg>")

    with open(path, "w") as f:
        f.write("\n".join(parts))
    return path


if __name__ == "__main__":
    here = os.path.dirname(os.path.abspath(__file__))
    root = os.path.dirname(here)
    res = os.path.join(root, "android/app/src/main/res")

    render_svg(os.path.join(here, "logo.svg"))
    render_vector(os.path.join(res, "drawable/ic_mesh.xml"))

    # The website uses the same mark rather than an approximation of it. It
    # gets both the file and the favicon, because a site that draws its own
    # mushroom drifts from the app's within a week — which is exactly what
    # happened before this line existed.
    site = os.path.join(root, "site")
    if os.path.isdir(site):
        render_svg(os.path.join(site, "logo.svg"))
        render_svg(os.path.join(site, "favicon.svg"))

    # Legacy launcher bitmaps, the Basecamp package icon, and a big one for
    # F-Droid, which cannot extract an adaptive icon from an APK.
    for dpi, px in [("mdpi", 48), ("hdpi", 72), ("xhdpi", 96), ("xxxhdpi", 192)]:
        d = os.path.join(res, f"mipmap-{dpi}")
        os.makedirs(d, exist_ok=True)
        render_png(os.path.join(d, "ic_launcher.png"), px)
        render_png(os.path.join(d, "ic_launcher_round.png"), px)
    render_png(os.path.join(root, "basecamp/icon.png"), 512)
    render_png(os.path.join(here, "logo.png"), 512)
    print("wrote the mark: svg, vector drawable, mipmaps, basecamp icon, site")
