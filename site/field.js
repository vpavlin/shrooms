// The spore field, behind every page.
//
// The same drawing the app shows while a device is joining a mesh, and the
// same idea as the mark: a mesh is something that grows rather than something
// that is configured. Spores drift, filaments appear between the ones that
// happen to be close, and fade as they part — so the network assembles and
// dissolves without ever settling, which is honest about what a mesh is.
//
// Two rules carried over from the app, both learned the hard way:
//
//   Whole cycles only. The phase runs 0..2π and restarts, so anything that is
//   not an integer multiple of it lands somewhere else when it wraps and the
//   whole field jumps at once. On the phone that produced a teleport every
//   nineteen seconds that read as a rendering bug.
//
//   Decoration must cost nothing anybody notices. A fixed number of points, no
//   per-frame allocation, and nothing at all for a visitor who has asked for
//   reduced motion.

(function () {
    const canvas = document.getElementById("field");
    if (!canvas) return;

    const reduced = window.matchMedia("(prefers-reduced-motion: reduce)");
    if (reduced.matches) return;

    const ctx = canvas.getContext("2d", { alpha: true });
    const PHOSPHOR = "53, 240, 160";
    const VIOLET = "154, 123, 255";

    // A fixed seed: the same drift on every visit, so the page has a character
    // rather than being different noise each time.
    let seed = 0x5eed;
    function rnd() {
        // xorshift, because Math.random cannot be seeded and a field that
        // reshuffles on reload is a field nobody recognises.
        seed ^= seed << 13; seed ^= seed >>> 17; seed ^= seed << 5;
        return ((seed >>> 0) % 100000) / 100000;
    }

    const COUNT = window.innerWidth < 700 ? 14 : 26;
    const spores = Array.from({ length: COUNT }, () => ({
        x: rnd(),
        y: rnd(),
        r: 0.03 + rnd() * 0.10,          // how far it wanders
        phase: rnd() * Math.PI * 2,
        kx: 1 + Math.floor(rnd() * 2),   // integer harmonics, see above
        ky: 2 + Math.floor(rnd() * 2),
        size: 1.4 + rnd() * 2.4,
        violet: rnd() < 0.22,
    }));

    let w = 0, h = 0, dpr = 1;
    function resize() {
        dpr = Math.min(window.devicePixelRatio || 1, 2);
        w = canvas.clientWidth = window.innerWidth;
        h = canvas.clientHeight = window.innerHeight;
        canvas.width = Math.floor(w * dpr);
        canvas.height = Math.floor(h * dpr);
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    }
    resize();
    window.addEventListener("resize", resize, { passive: true });

    // One phase for everything, 19 seconds a cycle, so nothing can drift out of
    // step with anything else.
    const PERIOD = 19000;
    const pts = spores.map(() => ({ x: 0, y: 0 }));

    function frame(ms) {
        const t = ((ms % PERIOD) / PERIOD) * Math.PI * 2;
        ctx.clearRect(0, 0, w, h);

        for (let i = 0; i < spores.length; i++) {
            const s = spores[i];
            pts[i].x = (s.x + s.r * Math.cos(s.kx * t + s.phase)) * w;
            pts[i].y = (s.y + s.r * Math.sin(s.ky * t + s.phase)) * h;
        }

        // Filaments first, so a spore is never drawn under a line it does not
        // touch. Strongest as two pass closest, which is what makes the field
        // look like it is connecting rather than merely moving.
        const near = Math.min(w, h) * 0.30;
        ctx.lineWidth = 1;
        for (let i = 0; i < pts.length; i++) {
            for (let j = i + 1; j < pts.length; j++) {
                const dx = pts[i].x - pts[j].x;
                const dy = pts[i].y - pts[j].y;
                const d = Math.hypot(dx, dy);
                if (d > near) continue;
                const k = 1 - d / near;
                ctx.strokeStyle = `rgba(${PHOSPHOR}, ${0.03 + 0.14 * k * k})`;
                ctx.beginPath();
                ctx.moveTo(pts[i].x, pts[i].y);
                ctx.lineTo(pts[j].x, pts[j].y);
                ctx.stroke();
            }
        }

        for (let i = 0; i < pts.length; i++) {
            const s = spores[i];
            const c = s.violet ? VIOLET : PHOSPHOR;
            ctx.fillStyle = `rgba(${c}, 0.07)`;
            ctx.beginPath();
            ctx.arc(pts[i].x, pts[i].y, s.size * 3.4, 0, Math.PI * 2);
            ctx.fill();
            ctx.fillStyle = `rgba(${c}, 0.5)`;
            ctx.beginPath();
            ctx.arc(pts[i].x, pts[i].y, s.size, 0, Math.PI * 2);
            ctx.fill();
        }

        requestAnimationFrame(frame);
    }
    requestAnimationFrame(frame);
})();
