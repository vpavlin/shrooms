#!/usr/bin/env python3
"""Generate the Shrooms mark: a mushroom whose gills are the mesh.

One geometry, three outputs — a PNG with real glow, an Android VectorDrawable
for the adaptive icon, and an SVG for documents. Generated rather than drawn so
the launcher icon, the package icon and the README cannot drift apart, which is
exactly what happens when a designer hands over four PNGs and someone later
edits one.

    python3 assets/logo.py

Deterministic: a fixed seed, so regenerating produces the same mark. A logo
that reshuffles itself on every build is not a logo.
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
CX, CY = 54.0, 56.0          # where the stem meets the cap
CAP_W, CAP_H = 38.0, 30.0    # cap radii — a dome, not a saucer
STEM_TOP, STEM_BOT = 56.0, 93.0

# Deep forest and earth, with the app's own neon for the living parts.
VOID = (7, 9, 11)
FOREST = (24, 58, 41)
FOREST_DARK = (12, 32, 22)
EARTH = (58, 42, 31)
EARTH_DARK = (30, 22, 16)
PHOSPHOR = (53, 240, 160)
VIOLET = (154, 123, 255)
BONE = (214, 221, 227)


def cap_point(t):
    """A point on the cap's rim, t in [0, 1] left to right."""
    a = math.pi * (1 + t)
    return CX + CAP_W * math.cos(a), CY + CAP_H * math.sin(a)


def mesh(rings=None):
    """Nodes and edges forming the gills.

    Gills radiate from where the stem meets the cap, so the graph is built the
    same way: rings of nodes at increasing radius, each linked inward. The
    result reads as gills at a glance and as a network when you look, which is
    the whole idea.
    """
    rnd = random.Random(20260810)
    nodes = [(CX, CY - 2.0)]          # the hub, just under the cap's centre
    # Optical sizing. At 48px the full graph turns to mush — a green blob with
    # no mushroom in it — so small renders get fewer, larger nodes from the same
    # geometry rather than a second drawing that would drift.
    if rings is None:
        rings = [(0.42, 5, 3.4), (0.72, 7, 2.8), (0.97, 9, 2.2)]
    edges = []

    ring_start = [0, 1]
    for frac, count, _ in rings:
        start = len(nodes)
        for i in range(count):
            t = (i + 0.5) / count
            rx, ry = cap_point(t)
            # Pull toward the hub by `frac`, with a little jitter so it looks
            # grown rather than plotted.
            jitter = (rnd.uniform(-1.2, 1.2), rnd.uniform(-0.8, 0.8))
            x = CX + (rx - CX) * frac + jitter[0]
            y = CY + (ry - CY) * frac + jitter[1]
            # Keep every node inside the cap. Jitter pushed some past the rim,
            # and a node floating outside the silhouette reads as a mistake
            # rather than as a spore — the spores are drawn separately and
            # deliberately.
            nx, ny = (x - CX) / CAP_W, (y - CY) / CAP_H
            m = math.hypot(nx, ny)
            if m > 0.88:
                x = CX + nx / m * 0.88 * CAP_W
                y = CY + ny / m * 0.88 * CAP_H
            nodes.append((x, min(y, CY - 1.2)))
        ring_start.append(len(nodes))

        # Link each node to the nearest one in the previous ring: edges then
        # fan outward from the stem exactly as gills do.
        prev_lo, prev_hi = ring_start[-3], ring_start[-2]
        for j in range(start, len(nodes)):
            best, bd = prev_lo, 1e9
            for k in range(prev_lo, prev_hi):
                d = (nodes[j][0] - nodes[k][0]) ** 2 + (nodes[j][1] - nodes[k][1]) ** 2
                if d < bd:
                    best, bd = k, d
            edges.append((j, best))

        # A few sideways links, so it is a mesh and not a tree.
        for j in range(start + 1, len(nodes)):
            if rnd.random() < 0.55:
                edges.append((j, j - 1))

    radii = {}
    for idx, (frac, count, r) in enumerate(rings):
        for j in range(ring_start[idx + 1], ring_start[idx + 2]):
            radii[j] = r
    radii[0] = 4.2
    return nodes, edges, radii


def spores(n=7):
    """Spores drifting off the cap — the loose ends of the network."""
    rnd = random.Random(7)
    out = []
    for _ in range(n):
        a = rnd.uniform(math.pi * 1.05, math.pi * 1.95)
        r = rnd.uniform(1.15, 1.5)
        out.append((CX + CAP_W * r * math.cos(a) * 0.85,
                    CY + CAP_H * r * math.sin(a) * 1.1,
                    rnd.uniform(0.9, 1.9)))
    return out


# --- PNG ---------------------------------------------------------------------

# Below this, drop to the simplified graph and thicken everything.
SMALL = 128


def render_png(path, size, background=True):
    small = size < SMALL
    rings = [(0.55, 4, 5.0), (0.98, 6, 4.0)] if small else None
    weight = 1.9 if small else 1.0
    s = size / VIEW
    up = 4                                   # supersample, then downscale
    W = int(size * up)
    img = Image.new("RGBA", (W, W), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    def P(x, y):
        return (x * s * up, y * s * up)

    def ellipse(cx, cy, rx, ry, **kw):
        d.ellipse([P(cx - rx, cy - ry), P(cx + rx, cy + ry)], **kw)

    if background:
        d.rounded_rectangle([0, 0, W, W], radius=int(W * 0.22), fill=VOID + (255,))
        # A faint forest wash, so the mark is not floating on flat black.
        wash = Image.new("RGBA", (W, W), (0, 0, 0, 0))
        ImageDraw.Draw(wash).ellipse(
            [P(CX - 62, CY - 40), P(CX + 62, CY + 74)], fill=FOREST_DARK + (150,))
        wash = wash.filter(ImageFilter.GaussianBlur(W * 0.06))
        img.alpha_composite(wash)
        d = ImageDraw.Draw(img)

    # Stem first, so the cap sits on top of it. Tapering, and fading into the
    # void rather than stopping — the brief asked for a stem disappearing
    # downward, and a hard end would read as a chess piece.
    steps = 60
    seg = (STEM_BOT - STEM_TOP) / steps
    for i in range(steps):
        f = i / (steps - 1)
        y0 = STEM_TOP + (STEM_BOT - STEM_TOP) * f
        half = 7.6 - 2.8 * f
        a = int(255 * max(0.0, 1.0 - f * f * 1.05))
        c = tuple(int(EARTH[k] + (EARTH_DARK[k] - EARTH[k]) * f) for k in range(3))
        band = Image.new("RGBA", (W, W), (0, 0, 0, 0))
        ImageDraw.Draw(band).rectangle(
            [P(CX - half, y0), P(CX + half, y0 + seg + 0.4)], fill=c + (a,))
        img.alpha_composite(band)
    d = ImageDraw.Draw(img)

    # Cap: a pieslice, which gives a dome with a flat underside directly.
    # Erasing the lower half of an ellipse instead punched a transparent hole
    # straight through the background — the first render was a mushroom with a
    # white block for a stem.
    d.pieslice([P(CX - CAP_W, CY - CAP_H), P(CX + CAP_W, CY + CAP_H)],
               180, 360, fill=FOREST + (255,))
    # A lighter crown, inset, for a little roundness.
    d.pieslice([P(CX - CAP_W * 0.94, CY - CAP_H * 1.02), P(CX + CAP_W * 0.94, CY + CAP_H * 0.62)],
               180, 360, fill=(31, 74, 52, 255))
    # The lip: a shallow ellipse along the rim, so the cap has thickness.
    ellipse(CX, CY, CAP_W, CAP_H * 0.22, fill=FOREST_DARK + (255,))
    d.arc([P(CX - CAP_W, CY - CAP_H), P(CX + CAP_W, CY + CAP_H)], 180, 360,
          fill=(46, 104, 74, 255), width=max(1, int(0.8 * s * up)))

    nodes, edges, radii = mesh(rings)

    # Glow first, on its own layer, then the crisp mark on top.
    glow = Image.new("RGBA", (W, W), (0, 0, 0, 0))
    g = ImageDraw.Draw(glow)
    for a, b in edges:
        g.line([P(*nodes[a]), P(*nodes[b])], fill=PHOSPHOR + (200,),
               width=max(1, int(1.5 * weight * s * up)))
    for i, (x, y) in enumerate(nodes):
        r = radii.get(i, 2.2)
        g.ellipse([P(x - r, y - r), P(x + r, y + r)], fill=PHOSPHOR + (230,))
    for x, y, r in (() if small else spores()):
        g.ellipse([P(x - r, y - r), P(x + r, y + r)], fill=VIOLET + (220,))
    glow = glow.filter(ImageFilter.GaussianBlur(W * 0.018))
    img.alpha_composite(glow)

    d = ImageDraw.Draw(img)
    for a, b in edges:
        d.line([P(*nodes[a]), P(*nodes[b])], fill=PHOSPHOR + (255,),
               width=max(1, int(0.9 * weight * s * up)))
    for i, (x, y) in enumerate(nodes):
        r = radii.get(i, 2.2) * (0.72 if small else 0.55)
        d.ellipse([P(x - r, y - r), P(x + r, y + r)], fill=BONE + (255,))
        if not small:
            # The ring around each node is the first thing to vanish; at small
            # sizes it only muddies the dot it was meant to define.
            d.ellipse([P(x - r * 1.7, y - r * 1.7), P(x + r * 1.7, y + r * 1.7)],
                      outline=PHOSPHOR + (170,), width=max(1, int(0.5 * s * up)))
    for x, y, r in (() if small else spores()):
        d.ellipse([P(x - r * 0.6, y - r * 0.6), P(x + r * 0.6, y + r * 0.6)],
                  fill=VIOLET + (255,))

    img.resize((size, size), Image.LANCZOS).save(path)
    return path


# --- vector ------------------------------------------------------------------

def _paths():
    """Shared path data for the vector outputs."""
    cap = (f"M {CX - CAP_W} {CY} "
           f"A {CAP_W} {CAP_H} 0 0 1 {CX + CAP_W} {CY} Z")
    cap_lip = (f"M {CX - CAP_W} {CY} "
               f"A {CAP_W} {CAP_H * 0.30} 0 0 0 {CX + CAP_W} {CY} Z")
    stem = (f"M {CX - 7.4} {STEM_TOP} L {CX + 7.4} {STEM_TOP} "
            f"L {CX + 4.8} {STEM_BOT} L {CX - 4.8} {STEM_BOT} Z")
    nodes, edges, radii = mesh()
    wires = " ".join(
        f"M {nodes[a][0]:.2f} {nodes[a][1]:.2f} L {nodes[b][0]:.2f} {nodes[b][1]:.2f}"
        for a, b in edges)
    return cap, cap_lip, stem, nodes, edges, radii, wires


def render_vector(path):
    cap, cap_lip, stem, nodes, edges, radii, wires = _paths()

    def hexa(c):
        return "#%02X%02X%02X" % c

    dots = []
    for i, (x, y) in enumerate(nodes):
        r = radii.get(i, 2.2) * 0.55
        # A circle as two arcs: VectorDrawable has no circle element.
        dots.append(f"M {x - r:.2f} {y:.2f} a {r:.2f} {r:.2f} 0 1 0 {2 * r:.2f} 0 "
                    f"a {r:.2f} {r:.2f} 0 1 0 {-2 * r:.2f} 0 Z")
    sp = []
    for x, y, r in spores():
        r *= 0.6
        sp.append(f"M {x - r:.2f} {y:.2f} a {r:.2f} {r:.2f} 0 1 0 {2 * r:.2f} 0 "
                  f"a {r:.2f} {r:.2f} 0 1 0 {-2 * r:.2f} 0 Z")

    xml = f'''<?xml version="1.0" encoding="utf-8"?>
<!-- Generated by assets/logo.py. Do not edit by hand; edit the generator. -->
<vector xmlns:android="http://schemas.android.com/apk/res/android"
    android:width="108dp" android:height="108dp"
    android:viewportWidth="108" android:viewportHeight="108">

    <!-- stem, fading into the void -->
    <path android:pathData="{stem}" android:fillColor="{hexa(EARTH)}" android:fillAlpha="0.92"/>

    <!-- cap -->
    <path android:pathData="{cap}" android:fillColor="{hexa(FOREST)}"/>
    <path android:pathData="{cap_lip}" android:fillColor="{hexa(FOREST_DARK)}"/>

    <!-- the gills are the mesh: a soft pass under a crisp one, since a
         VectorDrawable cannot blur -->
    <path android:pathData="{wires}" android:strokeColor="{hexa(PHOSPHOR)}"
        android:strokeWidth="2.2" android:strokeAlpha="0.28" android:strokeLineCap="round"/>
    <path android:pathData="{wires}" android:strokeColor="{hexa(PHOSPHOR)}"
        android:strokeWidth="0.9" android:strokeAlpha="0.95" android:strokeLineCap="round"/>

    <path android:pathData="{' '.join(dots)}" android:fillColor="{hexa(BONE)}"/>
    <path android:pathData="{' '.join(sp)}" android:fillColor="{hexa(VIOLET)}"/>
</vector>
'''
    with open(path, "w") as f:
        f.write(xml)
    return path


def render_svg(path):
    cap, cap_lip, stem, nodes, edges, radii, wires = _paths()

    def hexa(c):
        return "#%02X%02X%02X" % c

    parts = [f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 108 108" width="512" height="512">',
             '<defs><filter id="g" x="-50%" y="-50%" width="200%" height="200%">'
             '<feGaussianBlur stdDeviation="1.6"/></filter></defs>',
             f'<rect width="108" height="108" rx="24" fill="{hexa(VOID)}"/>',
             f'<path d="{sx.escape(stem)}" fill="{hexa(EARTH)}" opacity="0.92"/>',
             f'<path d="{sx.escape(cap)}" fill="{hexa(FOREST)}"/>',
             f'<path d="{sx.escape(cap_lip)}" fill="{hexa(FOREST_DARK)}"/>',
             f'<g filter="url(#g)" opacity="0.85"><path d="{sx.escape(wires)}" '
             f'stroke="{hexa(PHOSPHOR)}" stroke-width="2.4" fill="none" stroke-linecap="round"/></g>',
             f'<path d="{sx.escape(wires)}" stroke="{hexa(PHOSPHOR)}" stroke-width="0.9" '
             f'fill="none" stroke-linecap="round"/>']
    for i, (x, y) in enumerate(nodes):
        r = radii.get(i, 2.2) * 0.55
        parts.append(f'<circle cx="{x:.2f}" cy="{y:.2f}" r="{r:.2f}" fill="{hexa(BONE)}"/>')
    for x, y, r in spores():
        parts.append(f'<circle cx="{x:.2f}" cy="{y:.2f}" r="{r * 0.6:.2f}" '
                     f'fill="{hexa(VIOLET)}"/>')
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
