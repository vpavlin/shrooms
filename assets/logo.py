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
VIOLET = (154, 123, 255)      # spores, and "relayed" everywhere else
# The stem where it has bruised: violet mixed into the pale flesh rather than
# laid over it, because that is what bruising is.
BRUISE = (150, 140, 205)
BONE = (214, 221, 227)

# Three fruiting bodies, and none of them standing to attention.
#
# The first version of this was read as "a castle with three towers", which was
# fair and is worth recording because every part of it was a decision:
#
#   Straight vertical stems of even width are columns. Real ones bend, so these
#   bend — each has a sway as well as a lean, and no two the same.
#
#   A cone is a tower roof. The cap profile is a dome now, widest low down,
#   with a rim that curls under; the overhang is the thing that says mushroom,
#   and a straight-sided cap has none.
#
#   Tall-in-the-middle-flanked-by-two is a castle silhouette before it is
#   anything else. They cluster off-centre instead, at three different heights,
#   with one leaning across another.
#
#   x, cap half-width, cap height, stem height, lean, sway
SHROOMS = [
    (47.0, 9.6, 21.0, 26.0, 0.34, -0.34),
    (70.0, 6.8, 14.5, 17.0, -0.38, 0.30),
    (31.0, 5.4, 11.0, 10.0, 0.22, 0.34),
]


def stem_axis(x0, stem_h, lean, sway, y):
    """Where a stem's centre line is at height y.

    One function, used by the stem outline and by whatever sits on top of it.
    They were two expressions that had to agree, and the day they stopped
    agreeing a cap ended up beside its stem rather than on it.
    """
    top = HORIZON - stem_h
    t = 0.0 if stem_h <= 0 else (y - top) / (HORIZON - top)
    t = max(0.0, min(1.0, t))
    # Lean tilts the top away from the foot; sway bows the middle, which is
    # what stops it reading as a drawn line rather than a grown thing.
    return (x0
            + lean * (1.0 - t) * stem_h * 0.55
            + sway * math.sin(math.pi * t) * stem_h * 0.34)

def cap_outline(x0, w, h, base_y, n=26):
    """A liberty cap: a tall bell with a flared rim and a point on top.

    Parameterised by height rather than by width, which is the whole of why
    this works. Sweeping x and solving for y gives a shape that is wide near
    the apex — a bullet, or a thimble. Sweeping height and solving for width
    gives sides that start narrow and widen all the way down, which is what a
    bell is.

    The exponent decides everything else. One is a straight cone, and a cone
    with a stem under it is a tower with a roof — which is what the first
    version of this mark was read as. Below one the sides bulge outward and it
    reads as grown rather than built. The rim then kicks out and turns under in
    the last few percent of the height, because a psilocybe's margin is everted
    and because the overhang is what makes it a mushroom at a glance.
    """
    pts = []
    curl = h * 0.09          # how far the rim turns under

    def half(v):
        """Half-width at v, where v is 0 at the rim and 1 at the apex."""
        # A square root, which is a rounded crown: near the apex the width
        # falls off as the square of the height, so the top is a dome. Higher
        # exponents converge to a point instead, and a point on top of a stem
        # is a spire — 0.68 was tried and still read as a roof.
        body = (1.0 - v) ** 0.5
        # The everted margin, over the bottom tenth only. A psilocybe's rim
        # turns out before it turns under.
        return body * (1.0 + 0.16 * math.exp(-v / 0.08))

    def drop(v):
        return curl * math.exp(-v / 0.05)

    # Up the left side, over the apex, and down the right. The apex is rounded
    # by the profile itself and carries a papilla only as a slight rise — an
    # explicit point above the crown turned the whole cap back into a triangle,
    # which is the thing this shape exists to avoid.
    for i in range(n + 1):
        v = i / n
        pts.append((x0 - w * half(v), base_y - h * v + drop(v)))
    for i in range(n + 1):
        v = 1.0 - i / n
        pts.append((x0 + w * half(v), base_y - h * v + drop(v)))

    # The underside, as its own shape rather than the tail of this one. The
    # renderers used to take the second half of the point list for it, which
    # worked only as long as nobody changed the order — and when somebody did,
    # every cap grew a dark diagonal wedge across it.
    rim = w * half(0.0)
    under = []
    # Out along the rim, which hangs lowest at its edges...
    for i in range(n + 1):
        u = -1.0 + 2.0 * i / n
        under.append((x0 + rim * u, base_y + curl * (u * u)))
    # ...and back along the gills, which rise towards the stem. The gap between
    # the two is the overhang.
    for i in range(n + 1):
        u = 1.0 - 2.0 * i / n
        under.append((x0 + rim * u, base_y - h * 0.17 * (1.0 - u * u)))
    return pts, under


def stem_outline(x0, half, stem_h, lean, sway, top_y, bot_y, n=20):
    """A slender stem that bends, tapering towards the cap.

    Sampled off stem_axis rather than carrying its own idea of where the stem
    is, so the curve and the cap cannot drift apart.
    """
    left, right = [], []
    for i in range(n + 1):
        t = i / n
        y = top_y + (bot_y - top_y) * t
        cx = stem_axis(x0, stem_h, lean, sway, y)
        # Wider at the foot and narrowest just under the cap, which is the way
        # round real ones are and the opposite of a column.
        wdt = half * (0.86 + 0.5 * t * t)
        left.append((cx - wdt, y))
        right.append((cx + wdt, y))
    return left + right[::-1]


def stem_foot(x0, half, stem_h, lean, sway, bot_y, tall=6.0, n=8):
    """The bruised section of stem just above the soil.

    A piece of the stem rather than a mark across it. Psilocybes go blue where
    they are handled or damaged, which is a stain through the flesh — and a bar
    of a second colour laid over a stem reads as a band on a straw. Built from
    stem_axis like everything else, so it follows the bend.
    """
    top_y = bot_y - tall
    left, right = [], []
    for i in range(n + 1):
        t = i / n
        y = top_y + (bot_y - top_y) * t
        cx = stem_axis(x0, stem_h, lean, sway, y)
        tt = (y - (HORIZON - stem_h)) / max(1e-6, stem_h)
        wdt = half * (0.86 + 0.5 * tt * tt) * 1.02
        left.append((cx - wdt, y))
        right.append((cx + wdt, y))
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
    for x0, _w, _h, sh, lean, sway in SHROOMS:
        # Where the stem actually meets the soil, which with a bent stem is not
        # where its nominal x is.
        foot = (stem_axis(x0, sh, lean, sway, HORIZON), HORIZON)
        near = min(nodes, key=lambda n: (n[0] - foot[0]) ** 2 + (n[1] - foot[1]) ** 2)
        roots.append((foot, near))

    return nodes, edges, roots


def spores(n=6):
    """Spores drifting off the tallest cap — how a mesh gains a member."""
    rnd = random.Random(7)
    x0, w, h, sh, lean, sway = SHROOMS[0]
    x0 = stem_axis(x0, sh, lean, sway, HORIZON - sh)
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
SHROOMS_SMALL = [(54.0, 21.0, 24.0, 14.0, 0.0, 0.0)]
NODES_SMALL = [(23.0, 72.0), (42.0, 85.0), (67.0, 83.0), (86.0, 70.0)]
EDGES_SMALL = [(0, 1), (1, 2), (2, 3), (0, 2)]


def _geometry(simple=False):
    """Everything the three renderers draw, computed once."""
    shrooms = SHROOMS_SMALL if simple else SHROOMS
    caps, stems, feet = [], [], []
    for x0, w, h, sh, lean, sway in shrooms:
        base = HORIZON - sh
        # The cap goes where the top of the stem actually is, not where a
        # straight stem would have put it.
        cap, under = cap_outline(stem_axis(x0, sh, lean, sway, base), w, h, base)
        caps.append((cap, under))
        halfw = max(1.15, w * (0.155 if simple else 0.125))
        stems.append(stem_outline(x0, halfw, sh, lean, sway,
                                  base - h * 0.05, HORIZON + 1.5))
        feet.append(stem_foot(x0, halfw, sh, lean, sway, HORIZON + 1.5))
    if simple:
        foot = (SHROOMS_SMALL[0][0], HORIZON)
        near = min(NODES_SMALL, key=lambda n: (n[0] - foot[0]) ** 2 + (n[1] - foot[1]) ** 2)
        return caps, stems, feet, NODES_SMALL, EDGES_SMALL, [(foot, near)]

    nodes, edges, roots = mycelium()
    return caps, stems, feet, nodes, edges, roots


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
    caps, stems, feet, nodes, edges, roots = _geometry(simple=not big)
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
        # The bruise: a stained section of the stem, not a band across it.
        poly(feet[k], BRUISE)
        poly(caps[k][0], CAP)
        # A darker underside, so the cap has a lip rather than being a flat
        # shape stuck on a stick.
        poly(caps[k][1], CAP_DARK)

    if big:
        for x, y, rr in spores():
            cx, cy = P(x, y)
            rr = rr * s
            d.ellipse([cx - rr, cy - rr, cx + rr, cy + rr], fill=VIOLET + (220,))

    img.save(path)
    return path


# --- Android VectorDrawable --------------------------------------------------

def render_vector(path):
    caps, stems, feet, nodes, edges, roots = _geometry()

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
        # The bruised foot. This drawing had it and the other two did not,
        # which is how three renderers of one mark quietly become three marks.
        parts.append(f'    <path android:pathData="{_poly(feet[k])}" '
                     f'android:fillColor="{hexa(BRUISE)}"/>')
        parts.append(f'    <path android:pathData="{_poly(caps[k][0])}" '
                     f'android:fillColor="{hexa(CAP)}"/>')
        parts.append(f'    <path android:pathData="{_poly(caps[k][1])}" '
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
    caps, stems, feet, nodes, edges, roots = _geometry()

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
        parts.append(f'<path d="{sx.escape(_poly(feet[k]))}" fill="{hexa(BRUISE)}"/>')
        parts.append(f'<path d="{sx.escape(_poly(caps[k][0]))}" fill="{hexa(CAP)}"/>')
        parts.append(f'<path d="{sx.escape(_poly(caps[k][1]))}" '
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
