package xyz.vpavlin.shrooms

import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.drawText
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlin.math.PI
import kotlin.math.cos
import kotlin.math.hypot
import kotlin.math.min
import kotlin.math.sin

/**
 * The mesh as it actually is.
 *
 * Worth drawing because the topology is the interesting part and a list cannot
 * show it: which peers you reach directly, which go through a relay, and which
 * are talking but unreachable. Those are three different problems and they look
 * identical in a list.
 *
 * This device sits in the centre — the graph is one node's view, not a map of
 * the whole mesh, and pretending otherwise would be a lie since no node knows
 * how any two others reach each other.
 */
@Composable
fun MeshGraph(
    snap: Snapshot,
    onSelect: (Peer?) -> Unit,
    modifier: Modifier = Modifier,
    /**
     * Draw the links between other peers as well as our own.
     *
     * Those are inferred, not measured: a node knows its own tunnels and
     * nothing about anyone else's, because no announce carries them. Two peers
     * both reachable from here may still only reach each other through a relay
     * — which is exactly why the relay exists. So they are drawn faintly, and
     * only within a mesh, since peers on different meshes genuinely cannot
     * reach each other at all.
     */
    inferred: Boolean = false,
) {
    val measurer = rememberTextMeasurer()

    // A slow pulse on live links. Motion only where traffic is actually
    // flowing, so an idle screen is still.
    val pulse by rememberInfiniteTransition(label = "pulse").animateFloat(
        initialValue = 0f,
        targetValue = 1f,
        animationSpec = infiniteRepeatable(tween(2600, easing = LinearEasing), RepeatMode.Restart),
        label = "pulse",
    )

    // The slow wander, shared by the peers and the spores behind them. Much
    // slower than the pulse: the pulse is traffic, this is the thing being
    // alive. Same period as the joining screen, so the two read as one place.
    val drift by rememberInfiniteTransition(label = "drift").animateFloat(
        initialValue = 0f,
        targetValue = 2f * PI.toFloat(),
        animationSpec = infiniteRepeatable(tween(19_000, easing = LinearEasing), RepeatMode.Restart),
        label = "wander",
    )

    // Spores drifting behind the mesh, at a fraction of the joining screen's
    // presence. There they are the subject; here they are the substrate the
    // mesh grows in, and anything more would compete with the peers.
    val motes = remember { sporeField(9) }

    // Where each peer was actually drawn, for hit testing. A plain list rather
    // than state: the draw pass writes it and the tap handler reads it, and
    // neither is composition — making it observable would recompose on every
    // frame for nothing.
    val drawn = remember { mutableListOf<Offset>() }

    // Where each peer is being drawn right now, as distinct from where the
    // ring says it belongs. Written by the draw pass, like drawn.
    val eased = remember { mutableMapOf<String, Offset>() }

    // Grouped by mesh, so each mesh occupies an arc of the ring rather than
    // being scattered around it. Order is stable, so nodes do not jump between
    // refreshes.
    val peers = snap.peers.sortedWith(compareBy({ it.mesh }, { it.name }))
    val meshNames = peers.map { it.mesh }.distinct()
    // A relay is drawn where traffic through it would pass, so relayed links
    // visibly bend around it rather than crossing the middle.
    val relay = peers.firstOrNull { it.relay }

    Box(modifier.fillMaxSize().padding(16.dp)) {
        Canvas(
            Modifier.fillMaxSize().pointerInput(peers) {
                detectTapGestures { tap ->
                    // Against where the nodes are now, not where the ring says
                    // they would be: they wander, and a tap target that does
                    // not follow the thing you can see is worse than none.
                    val hit = drawn.indices.firstOrNull { i ->
                        hypot(tap.x - drawn[i].x, tap.y - drawn[i].y) < 60f
                    }
                    onSelect(hit?.let { peers.getOrNull(it) })
                }
            }
        ) {
            val centre = Offset(size.width / 2f, size.height / 2f)
            val radius = min(size.width, size.height) / 2f * 0.66f

            // The substrate, first and faintest.
            drawSpores(motes, drift, alpha = 0.22f)

            // Every position for this frame, computed once: the links, the
            // nodes and the hit test must agree, and recomputing the wander
            // three times invites them not to.
            // Where each node is going, and where it actually is. The ring
            // position depends on how many peers there are, so one arriving or
            // leaving moves all of them — instantly, and by a lot. Easing
            // towards the target turns that into the ring opening up to make
            // room, which is both prettier and easier to follow.
            val at = peers.mapIndexed { i, p ->
                val target = nodeAt(centre, radius, i, peers.size) +
                    wander(p, drift, radius * 0.085f)
                val key = p.mesh + "/" + p.name
                val cur = eased[key]
                val next = if (cur == null) target else cur + (target - cur) * 0.12f
                eased[key] = next
                next
            }
            // Anything no longer on the ring stops being remembered, or a peer
            // that comes back would slide in from where it left months ago.
            eased.keys.retainAll(peers.map { it.mesh + "/" + it.name }.toSet())
            drawn.clear()
            drawn.addAll(at)

            // The relay each mesh has, not the first relay on the screen.
            // Traffic to a peer on one mesh cannot pass through a relay on
            // another — different prefix, different WireGuard device — so
            // bending the line through one drew a path that cannot exist, and
            // the graph is here to show which path traffic actually takes.
            val relayIndex = peers.indices
                .filter { peers[it].relay }
                .associateBy { peers[it].mesh }

            peers.forEachIndexed { i, peer ->
                val r = relayIndex[peer.mesh]
                val relayAt = if (r != null && r != i) at[r] else null
                drawLink(centre, at[i], peer, relayAt, pulse, drift)
            }

            // Assumed links underneath everything: same mesh, both reachable
            // from here, so probably reachable from each other.
            if (inferred) {
                peers.forEachIndexed { i, a ->
                    if (a.reach != Peer.Reach.CONNECTED) return@forEachIndexed
                    peers.forEachIndexed inner@{ j, b ->
                        if (j <= i || b.reach != Peer.Reach.CONNECTED) return@inner
                        if (a.mesh != b.mesh) return@inner
                        // Faint enough to read as a guess, visible enough to
                        // read at all. A tenth of an alpha was honest and
                        // invisible, which is not a useful combination.
                        val tint = meshTint(a.mesh, meshNames)
                        drawFilament(
                            at[i], at[j],
                            tint.copy(alpha = 0.34f),
                            width = 1.4f,
                            bend = bendOf(i * 31 + j, drift),
                            effect = PathEffect.dashPathEffect(floatArrayOf(5f, 9f), 0f),
                        )
                    }
                }
            }

            // Peers on top of the links, each ringed by its mesh.
            peers.forEachIndexed { i, peer ->
                drawMeshRing(at[i], peer.mesh, meshNames)
                drawNode(at[i], peer, measurer)
            }

            // This device last: it is the thing the rest is drawn relative to.
            // It breathes rather than wanders — everything else moves around
            // it, which is exactly what the graph means.
            val breath = 1f + 0.06f * sin(2f * drift)
            drawCircle(Palette.Void, radius = 26f, center = centre)
            drawCircle(Palette.Bone, radius = 13f * breath, center = centre)
            drawCircle(
                Palette.Bone.copy(alpha = 0.18f),
                radius = 26f * breath,
                center = centre,
                style = Stroke(1.5f),
            )
        }
    }
}

private fun nodeAt(centre: Offset, radius: Float, index: Int, count: Int): Offset {
    if (count == 0) return centre
    // Start at the top and go clockwise; a single peer sits directly above,
    // which reads better than off to one side.
    //
    // Peers arrive grouped by mesh, so meshes come out as arcs rather than
    // interleaved — which, with the ring tint below, is what makes a device on
    // two meshes readable at a glance.
    val angle = (-Math.PI / 2) + (2 * Math.PI * index / count)
    return Offset(
        centre.x + radius * cos(angle).toFloat(),
        centre.y + radius * sin(angle).toFloat(),
    )
}

/**
 * Draws the path traffic actually takes.
 *
 * A relayed peer is drawn as two segments through the relay, because that is
 * where the packets go. Drawing it as a straight line would show a connection
 * that does not exist.
 */
/**
 * How far a peer has drifted from its place on the ring, right now.
 *
 * Derived from the peer's name and mesh rather than from a random seed, so a
 * node keeps its own motion across refreshes instead of jumping every time the
 * roster is polled. Small enough that the ring is still a ring: the grouping by
 * mesh has to survive, and a graph where everything swims is unreadable.
 */
private fun wander(peer: Peer, t: Float, scale: Float): Offset {
    val seed = (peer.name + "/" + peer.mesh).hashCode()
    val phase = ((seed and 0xffff) / 65535f) * 2f * PI.toFloat()
    val amp = scale * (0.55f + ((seed ushr 8) and 0xff) / 255f * 0.45f)

    // Whole numbers of cycles, always.
    //
    // t runs 0..2π and restarts, so anything that is not an integer multiple
    // of it lands somewhere else when it wraps and every node teleports —
    // once every nineteen seconds, which is exactly long enough to look like a
    // redraw glitch rather than the animation's own doing. Two different
    // integers give a Lissajous path rather than a circle, which is the part
    // that made it look alive, and it closes perfectly.
    val kx = 1 + ((seed ushr 16) and 0x1)          // 1 or 2
    val ky = 2 + ((seed ushr 20) and 0x1)          // 2 or 3
    return Offset(amp * cos(kx * t + phase), amp * sin(ky * t + phase))
}

/** How much a filament bows, swaying slowly and deterministically. */
private fun bendOf(seed: Int, t: Float): Float {
    val phase = ((seed * 2654435761u.toInt()) and 0xffff) / 65535f * 2f * PI.toFloat()
    // One cycle per drift period, for the reason in wander: a filament that
    // snaps back to a different curvature is the same glitch, on the links.
    return 0.06f + 0.05f * sin(t + phase)
}

/**
 * A link drawn as a curve rather than a straight line.
 *
 * Mycelium does not run in straight lines, and neither does anything else that
 * grows. This is only decoration — the curve carries no information the line
 * did not — but the graph is the screen people actually look at, and a thing
 * that looks alive gets looked at.
 */
private fun DrawScope.drawFilament(
    a: Offset,
    b: Offset,
    colour: Color,
    width: Float,
    bend: Float,
    effect: PathEffect? = null,
) {
    val c = controlPoint(a, b, bend)
    val path = Path().apply {
        moveTo(a.x, a.y)
        quadraticTo(c.x, c.y, b.x, b.y)
    }
    drawPath(path, colour, style = Stroke(width = width, pathEffect = effect))
}

/** The control point that bows a curve perpendicular to its own direction. */
private fun controlPoint(a: Offset, b: Offset, bend: Float): Offset {
    val dx = b.x - a.x
    val dy = b.y - a.y
    return Offset((a.x + b.x) / 2f - dy * bend, (a.y + b.y) / 2f + dx * bend)
}

/** A point along the same curve, for anything that travels down it. */
private fun alongFilament(a: Offset, b: Offset, bend: Float, t: Float): Offset {
    val c = controlPoint(a, b, bend)
    val u = 1f - t
    return Offset(
        u * u * a.x + 2f * u * t * c.x + t * t * b.x,
        u * u * a.y + 2f * u * t * c.y + t * t * b.y,
    )
}

/**
 * A ring around a peer, tinted by which mesh it is on.
 *
 * Deliberately a ring rather than the node's own colour: the node says whether
 * the peer is reachable, which is what you look at first, and the mesh is the
 * grouping around it. On a device with one mesh nothing is drawn at all.
 */
private fun DrawScope.drawMeshRing(at: Offset, mesh: String, meshes: List<String>) {
    if (meshes.size < 2) return
    val tint = meshTint(mesh, meshes)
    drawCircle(tint.copy(alpha = 0.85f), radius = 18f, center = at, style = Stroke(2f))
}

private fun meshTint(mesh: String, meshes: List<String>): Color =
    meshTints[meshes.indexOf(mesh).coerceAtLeast(0) % meshTints.size]

/**
 * Which mesh, as a colour.
 *
 * Deliberately none of the status colours: green, amber, violet and red all
 * already mean something about whether a peer is reachable, and a ring in one
 * of them next to a node in another is unreadable — the first attempt tinted
 * the first mesh phosphor, which made its rings invisible against the nodes
 * they surrounded.
 */
internal val meshTints = listOf(Palette.Sky, Palette.Blossom, Palette.Chartreuse, Palette.Ash)

private fun DrawScope.drawLink(
    centre: Offset,
    at: Offset,
    peer: Peer,
    relayAt: Offset?,
    pulse: Float,
    drift: Float,
) {
    val dashed = PathEffect.dashPathEffect(floatArrayOf(9f, 11f), pulse * -20f)
    val bend = bendOf(peer.name.hashCode(), drift)

    when (peer.reach) {
        Peer.Reach.CONNECTED -> {
            if (peer.relayed && relayAt != null) {
                drawFilament(centre, relayAt, Palette.Violet.copy(alpha = 0.75f), 2.5f, bend, dashed)
                drawFilament(relayAt, at, Palette.Violet.copy(alpha = 0.75f), 2.5f, bend, dashed)
            } else if (peer.relayed) {
                drawFilament(centre, at, Palette.Violet.copy(alpha = 0.75f), 2.5f, bend, dashed)
            } else {
                drawFilament(centre, at, Palette.Phosphor.copy(alpha = 0.85f), 2.5f, bend)
                // A travelling dot, now following the curve it travels on:
                // direction is visible, and it only moves where a tunnel is
                // live. A halo, because a bare dot on a bright line vanishes.
                val p = alongFilament(centre, at, bend, pulse)
                drawCircle(Palette.Phosphor.copy(alpha = 0.25f), radius = 8f, center = p)
                drawCircle(Palette.Phosphor, radius = 3.5f, center = p)
            }
        }
        // Announcing but no tunnel: trying, not connected. Drawn faintly so it
        // is visibly not the same as the line above.
        Peer.Reach.REACHING ->
            drawFilament(centre, at, Palette.Amber.copy(alpha = 0.5f), 1.5f, bend, dashed)
        Peer.Reach.OFFLINE ->
            drawFilament(centre, at, Palette.Line, 1f, bend, dashed)
    }
}

private fun DrawScope.drawNode(
    at: Offset,
    peer: Peer,
    measurer: androidx.compose.ui.text.TextMeasurer,
) {
    val colour = when (peer.reach) {
        Peer.Reach.CONNECTED -> if (peer.relayed) Palette.Violet else Palette.Phosphor
        Peer.Reach.REACHING -> Palette.Amber
        Peer.Reach.OFFLINE -> Palette.Ash
    }

    // Punch the background out first so links do not run under the label.
    drawCircle(Palette.Void, radius = 22f, center = at)
    drawCircle(colour.copy(alpha = 0.14f), radius = 18f, center = at)
    drawCircle(colour, radius = 7f, center = at)
    if (peer.relay) {
        // A ring marks a relay: it is the node others depend on.
        drawCircle(Palette.Violet, radius = 18f, center = at, style = Stroke(1.5f))
    }

    val label = measurer.measure(
        peer.name,
        TextStyle(fontFamily = FontFamily.Monospace, fontSize = 11.sp, color = colour),
    )
    drawText(
        label,
        topLeft = Offset(at.x - label.size.width / 2f, at.y + 24f),
    )
}
