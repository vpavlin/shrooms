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
PHOSPHOR = (53, 240, 160)     # the living network
VIOLET = (154, 123, 255)      # spores, and "relayed" everywhere else
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
    (47.0, 14.0, 13.0, 25.0, 0.30, -0.30),
    (72.0, 9.6, 9.0, 16.5, -0.34, 0.30),
    (29.0, 7.4, 7.0, 10.5, 0.22, 0.34),
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

def gasket(a, b, c, depth, out):
    """Collect the inverted triangles of a Sierpinski gasket.

    The holes rather than the filled parts, because that is what gets drawn:
    one triangle in the cap's colour with these punched out of it. Three levels
    is 13 holes, which is as much as survives being 20 pixels wide.
    """
    if depth <= 0:
        return
    ab = ((a[0] + b[0]) / 2.0, (a[1] + b[1]) / 2.0)
    bc = ((b[0] + c[0]) / 2.0, (b[1] + c[1]) / 2.0)
    ca = ((c[0] + a[0]) / 2.0, (c[1] + a[1]) / 2.0)
    out.append([ab, bc, ca])
    gasket(a, ab, ca, depth - 1, out)
    gasket(ab, b, bc, depth - 1, out)
    gasket(ca, bc, c, depth - 1, out)


def cap_triangles(x0, w, h, base_y, depth):
    """A cap as a triangle with a gasket in it.

    Two things this project already had, put together. The site draws a
    Sierpinski gasket because triangles and fractals are what people actually
    report seeing, and because a gasket is a mesh subdividing — every vertex a
    node, every edge a link. The graph on both front-ends draws the same
    figure, one level deep.

    So the cap is that figure rather than a picture of a mushroom cap. It reads
    as a mushroom because of what is under it, which is how a mark works: the
    silhouette carries the meaning and the detail carries the character.

    Returns the outer triangle and the holes to punch out of it.
    """
    apex = (x0, base_y - h)
    left = (x0 - w, base_y)
    right = (x0 + w, base_y)
    holes = []
    gasket(apex, left, right, depth, holes)
    return [apex, left, right], holes


def stem_line(x0, stem_h, lean, sway, top_y, bot_y, n=16):
    """The stem as a polyline, to be drawn as a glowing line.

    It was a filled shape in a pale flesh colour, which is what a mushroom stem
    looks like and not what anything else in this project looks like. Every
    connection on both front-ends is a glowing phosphor line; a stem is the
    connection between what the network grew and the network itself, so it is
    drawn the same way. The mark then belongs to the same family as the app
    rather than being a drawing that happens to sit beside it.
    """
    pts = []
    for i in range(n + 1):
        t = i / n
        y = top_y + (bot_y - top_y) * t
        pts.append((stem_axis(x0, stem_h, lean, sway, y), y))
    return pts


def mycelium():
    """The network under the soil: nodes, links, and the root of each stem.

    Sparser than it was, and laid out like the graph the two front-ends draw
    rather than like a scatter. It had three rows of fifteen nodes with
    nearest-neighbour links and a few random long ones, which is a fair model
    of a mycelium and, at any size a launcher icon is shown, a green smudge.

    Eight nodes on a shallow arc now, each linked to its neighbours plus a few
    crossing links so it reads as a mesh and not a chain. The arc matters: it
    puts every node at a different depth, which gives the eye something to
    follow, and it curves up towards the stems it feeds.
    """
    nodes = [
        (18.0, 74.0), (31.0, 82.0), (45.0, 87.0), (59.0, 88.0),
        (73.0, 84.0), (86.0, 77.0), (38.0, 72.0), (66.0, 73.0),
    ]
    # The chain along the arc, then three links across it. Written out rather
    # than generated: eight nodes is few enough to place by hand, and a mark
    # that changes when somebody edits a random seed is not a mark.
    edges = [
        (0, 1), (1, 2), (2, 3), (3, 4), (4, 5),
        (0, 6), (6, 2), (6, 3), (3, 7), (7, 4), (7, 5),
    ]

    # Each stem's foot joins the nearest node, which is the whole idea of the
    # mark: what is above the ground grew out of what is below it.
    roots = []
    for x0, _w, _h, sh, lean, sway in SHROOMS:
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
SHROOMS_SMALL = [(54.0, 25.0, 21.0, 15.0, 0.0, 0.0)]
NODES_SMALL = [(23.0, 72.0), (42.0, 85.0), (67.0, 83.0), (86.0, 70.0)]
EDGES_SMALL = [(0, 1), (1, 2), (2, 3), (0, 2)]


def _geometry(simple=False):
    """Everything the three renderers draw, computed once."""
    shrooms = SHROOMS_SMALL if simple else SHROOMS
    caps, stems = [], []
    for k, (x0, w, h, sh, lean, sway) in enumerate(shrooms):
        base = HORIZON - sh
        # The tallest gets three levels, the others two and one. Depth costs
        # nothing to draw and everything to read: a small cap subdivided three
        # times is a smudge, and all three at the same depth looks printed.
        depth = (3, 2, 1)[min(k, 2)]
        # The cap goes where the top of the stem actually is, not where a
        # straight stem would have put it.
        caps.append(cap_triangles(stem_axis(x0, sh, lean, sway, base), w, h, base,
                                  2 if simple else depth))
        stems.append(stem_line(x0, sh, lean, sway, base - h * 0.02, HORIZON))
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

    # Everything that glows is drawn twice: once wide and blurred, once thin
    # and bright. Both the links below the soil and the stems above it, because
    # a stem is a connection too — it joins what the network grew to the
    # network — and drawing it in a different idiom is what made the old mark
    # look like a picture beside the app instead of part of it.
    glow = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    gd = ImageDraw.Draw(glow)
    width = max(1, int(size * (0.022 if big else 0.045)))
    for i, j in edges:
        gd.line([P(*nodes[i]), P(*nodes[j])], fill=PHOSPHOR + (150,), width=width)
    for foot, near in roots:
        gd.line([P(*foot), P(*near)], fill=PHOSPHOR + (170,), width=width)
    for k in range(len(stems)):
        gd.line([P(*p) for p in stems[k]], fill=PHOSPHOR + (150,),
                width=width, joint="curve")
    glow = glow.filter(ImageFilter.GaussianBlur(size * 0.016))
    img.alpha_composite(glow)

    thin = max(2, int(size * (0.008 if big else 0.022)))
    for i, j in edges:
        d.line([P(*nodes[i]), P(*nodes[j])], fill=PHOSPHOR + (235,), width=thin)
    for foot, near in roots:
        d.line([P(*foot), P(*near)], fill=PHOSPHOR + (255,), width=thin)

    r = size * (0.017 if big else 0.045)
    for x, y in nodes:
        cx, cy = P(x, y)
        d.ellipse([cx - r, cy - r, cx + r, cy + r], fill=BONE)

    # The soil line, drawn over the links so the ground reads as a surface.
    d.line([P(4, HORIZON), P(VIEW - 4, HORIZON)],
           fill=EARTH_LINE, width=max(1, int(size * 0.012)))

    # The stems, over the soil line, and the caps over those. Tallest last so
    # it sits in front.
    stem_w = max(2, int(size * (0.011 if big else 0.026)))
    for k in ([0] if len(caps) == 1 else [1, 2, 0]):
        d.line([P(*p) for p in stems[k]], fill=PHOSPHOR + (255,),
               width=stem_w, joint="curve")
        # The cap: one triangle with a gasket punched out of it. The holes are
        # filled with the background rather than a darker tint, so the figure
        # is the same subtraction the site draws and not a decoration on top.
        poly(caps[k][0], CAP)
        for hole in caps[k][1]:
            poly(hole, VOID)

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
    parts.append("")
    # The stems, as strokes rather than filled shapes, so they read as the same
    # kind of thing as the links below them.
    for k in (1, 2, 0):
        stem = _lines(list(zip(stems[k][:-1], stems[k][1:])))
        parts.append(f'    <path android:pathData="{stem}" '
                     f'android:strokeColor="{hexa(PHOSPHOR)}" android:strokeWidth="2.6" '
                     f'android:strokeAlpha="0.35" android:strokeLineCap="round"/>')
        parts.append(f'    <path android:pathData="{stem}" '
                     f'android:strokeColor="{hexa(PHOSPHOR)}" android:strokeWidth="1.3" '
                     f'android:strokeLineCap="round"/>')
        parts.append(f'    <path android:pathData="{_poly(caps[k][0])}" '
                     f'android:fillColor="{hexa(CAP)}"/>')
        # The gasket, punched out in the background colour. A vector drawable
        # has no even-odd subtraction worth relying on across API levels, so
        # the holes are drawn as shapes in the colour behind them — which is
        # flat here, above the soil, and therefore exact.
        for hole in caps[k][1]:
            parts.append(f'    <path android:pathData="{_poly(hole)}" '
                         f'android:fillColor="{hexa(VOID)}"/>')

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
        stem = _lines(list(zip(stems[k][:-1], stems[k][1:])))
        parts.append(f'<g filter="url(#g)" opacity="0.85"><path d="{sx.escape(stem)}" '
                     f'stroke="{hexa(PHOSPHOR)}" stroke-width="2.6" fill="none" '
                     f'stroke-linecap="round"/></g>')
        parts.append(f'<path d="{sx.escape(stem)}" stroke="{hexa(PHOSPHOR)}" '
                     f'stroke-width="1.2" fill="none" stroke-linecap="round"/>')
        parts.append(f'<path d="{sx.escape(_poly(caps[k][0]))}" fill="{hexa(CAP)}"/>')
        for hole in caps[k][1]:
            parts.append(f'<path d="{sx.escape(_poly(hole))}" fill="{hexa(VOID)}"/>')

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
