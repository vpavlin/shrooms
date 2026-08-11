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
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
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
                    val centre = Offset(size.width / 2f, size.height / 2f)
                    val radius = min(size.width, size.height) / 2f * 0.66f
                    val hit = peers.withIndex().firstOrNull { (i, _) ->
                        val p = nodeAt(centre, radius, i, peers.size)
                        hypot(tap.x - p.x, tap.y - p.y) < 60f
                    }
                    onSelect(hit?.value)
                }
            }
        ) {
            val centre = Offset(size.width / 2f, size.height / 2f)
            val radius = min(size.width, size.height) / 2f * 0.66f

            peers.forEachIndexed { i, peer ->
                val at = nodeAt(centre, radius, i, peers.size)
                val relayAt = relay?.let { r ->
                    val ri = peers.indexOf(r)
                    if (ri >= 0 && r != peer) nodeAt(centre, radius, ri, peers.size) else null
                }
                drawLink(centre, at, peer, relayAt, pulse)
            }

            // Assumed links first, underneath everything: same mesh, both
            // reachable from here, so probably reachable from each other.
            if (inferred) {
                peers.forEachIndexed { i, a ->
                    if (a.reach != Peer.Reach.CONNECTED) return@forEachIndexed
                    peers.forEachIndexed inner@{ j, b ->
                        if (j <= i || b.reach != Peer.Reach.CONNECTED) return@inner
                        if (a.mesh != b.mesh) return@inner
                        drawLine(
                            Palette.Phosphor.copy(alpha = 0.10f),
                            nodeAt(centre, radius, i, peers.size),
                            nodeAt(centre, radius, j, peers.size),
                            1f,
                        )
                    }
                }
            }

            // Peers on top of the links, each ringed by its mesh.
            peers.forEachIndexed { i, peer ->
                val at = nodeAt(centre, radius, i, peers.size)
                drawMeshRing(at, peer.mesh, meshNames)
                drawNode(at, peer, measurer)
            }

            // This device last: it is the thing the rest is drawn relative to.
            drawCircle(Palette.Void, radius = 26f, center = centre)
            drawCircle(Palette.Bone, radius = 13f, center = centre)
            drawCircle(Palette.Bone.copy(alpha = 0.18f), radius = 26f, center = centre, style = Stroke(1.5f))
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
 * A ring around a peer, tinted by which mesh it is on.
 *
 * Deliberately a ring rather than the node's own colour: the node says whether
 * the peer is reachable, which is what you look at first, and the mesh is the
 * grouping around it. On a device with one mesh nothing is drawn at all.
 */
private fun DrawScope.drawMeshRing(at: Offset, mesh: String, meshes: List<String>) {
    if (meshes.size < 2) return
    val i = meshes.indexOf(mesh).coerceAtLeast(0)
    val tint = meshTints[i % meshTints.size]
    drawCircle(tint.copy(alpha = 0.55f), radius = 17f, center = at, style = Stroke(1.5f))
}

private val meshTints = listOf(Palette.Phosphor, Palette.Amber, Palette.Violet, Palette.Rust)

private fun DrawScope.drawLink(centre: Offset, at: Offset, peer: Peer, relayAt: Offset?, pulse: Float) {
    val dashed = PathEffect.dashPathEffect(floatArrayOf(9f, 11f), pulse * -20f)

    when (peer.reach) {
        Peer.Reach.CONNECTED -> {
            if (peer.relayed && relayAt != null) {
                drawLine(Palette.Violet.copy(alpha = 0.75f), centre, relayAt, 2.5f, pathEffect = dashed)
                drawLine(Palette.Violet.copy(alpha = 0.75f), relayAt, at, 2.5f, pathEffect = dashed)
            } else if (peer.relayed) {
                drawLine(Palette.Violet.copy(alpha = 0.75f), centre, at, 2.5f, pathEffect = dashed)
            } else {
                drawLine(Palette.Phosphor.copy(alpha = 0.85f), centre, at, 2.5f)
                // A travelling dot: direction is visible, and it only moves
                // where a tunnel is live.
                val t = pulse
                drawCircle(
                    Palette.Phosphor,
                    radius = 3.5f,
                    center = Offset(
                        centre.x + (at.x - centre.x) * t,
                        centre.y + (at.y - centre.y) * t,
                    ),
                )
            }
        }
        // Announcing but no tunnel: trying, not connected. Drawn faintly so it
        // is visibly not the same as the line above.
        Peer.Reach.REACHING ->
            drawLine(Palette.Amber.copy(alpha = 0.5f), centre, at, 1.5f, pathEffect = dashed)
        Peer.Reach.OFFLINE ->
            drawLine(Palette.Line, centre, at, 1f, pathEffect = dashed)
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
