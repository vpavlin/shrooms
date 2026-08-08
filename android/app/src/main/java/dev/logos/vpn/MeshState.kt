package dev.logos.vpn

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import org.json.JSONObject

/**
 * The app's view of the mesh, parsed from the Go status snapshot.
 *
 * Deliberately a mirror of `status --json` rather than a set of bound types:
 * adding a field in Go must not be an API change on both sides. See ADR-016.
 */
data class Peer(
    val name: String,
    /** What this peer answers to on the mesh, e.g. `laptop.mesh`. */
    val dnsName: String,
    val overlay: String,
    val online: Boolean,
    val live: Boolean,
    val relay: Boolean,
    val relayed: Boolean,
    val rttMs: Long,
    val handshakeAgeS: Long,
    val rxBytes: Long,
    val txBytes: Long,
    val tunnelAfterS: Double,
) {
    /**
     * Three states, not two. A peer can be announcing yet unreachable, which is
     * a different problem from being offline, and conflating them is how you
     * spend an afternoon debugging the wrong plane.
     */
    enum class Reach { CONNECTED, REACHING, OFFLINE }

    val reach: Reach
        get() = when {
            live -> Reach.CONNECTED
            online -> Reach.REACHING
            else -> Reach.OFFLINE
        }

    val how: String
        get() = when {
            !live -> "no tunnel"
            relayed -> "relayed"
            else -> "direct"
        }
}

data class Rendezvous(val status: String, val ok: Boolean, val problem: String, val detail: String)

data class Snapshot(
    val connected: Boolean = false,
    val name: String = "",
    val dnsName: String = "",
    val overlay: String = "",
    val prefix: String = "",
    val peers: List<Peer> = emptyList(),
    val rendezvous: Rendezvous = Rendezvous("unknown", true, "", ""),
    val error: String = "",
) {
    fun notificationLine(): String {
        val up = peers.count { it.reach == Peer.Reach.CONNECTED }
        return if (peers.isEmpty()) "No peers yet" else "$up of ${peers.size} peers reachable"
    }
}

object MeshState {

    private val _snapshot = MutableStateFlow(Snapshot())
    val snapshot: StateFlow<Snapshot> = _snapshot

    private val _logs = MutableStateFlow<List<String>>(emptyList())
    val logs: StateFlow<List<String>> = _logs

    fun connected(overlay: String) {
        _snapshot.value = _snapshot.value.copy(connected = true, overlay = overlay, error = "")
    }

    fun disconnected() {
        // Keep the error. A failed start calls fail() and then stop(), and
        // resetting to a blank Snapshot here wiped the message on its way out —
        // leaving the UI saying "not connected" with no reason, which is the
        // single least useful thing it could say.
        _snapshot.value = Snapshot(error = _snapshot.value.error)
    }

    fun fail(message: String) {
        _snapshot.value = _snapshot.value.copy(connected = false, error = message)
    }

    fun log(level: String, message: String) {
        // Bounded: this is a diagnostic tail, not a log file.
        _logs.value = (_logs.value + "$level  $message").takeLast(200)
    }

    fun update(json: String) {
        val parsed = runCatching { parse(json) }.getOrNull() ?: return
        _snapshot.value = parsed.copy(
            connected = true,
            error = _snapshot.value.error,
        )
    }

    private fun parse(json: String): Snapshot {
        val o = JSONObject(json)
        if (!o.has("overlay")) return _snapshot.value

        val peers = mutableListOf<Peer>()
        o.optJSONArray("peers")?.let { arr ->
            for (i in 0 until arr.length()) {
                val p = arr.getJSONObject(i)
                peers += Peer(
                    name = p.optString("name"),
                    dnsName = p.optString("dns_name"),
                    overlay = p.optString("overlay"),
                    online = p.optBoolean("online"),
                    live = p.optBoolean("live"),
                    relay = p.optBoolean("relay"),
                    relayed = p.optBoolean("relayed"),
                    rttMs = p.optLong("rtt_ms"),
                    handshakeAgeS = p.optLong("handshake_age_s"),
                    rxBytes = p.optLong("rx_bytes"),
                    txBytes = p.optLong("tx_bytes"),
                    tunnelAfterS = p.optDouble("tunnel_after_s", 0.0),
                )
            }
        }

        val r = o.optJSONObject("rendezvous")
        return Snapshot(
            name = o.optString("name"),
            dnsName = o.optString("dns_name"),
            overlay = o.optString("overlay"),
            prefix = o.optString("prefix"),
            peers = peers.sortedBy { it.name },
            rendezvous = Rendezvous(
                status = r?.optString("status") ?: "unknown",
                ok = r?.optBoolean("ok") ?: true,
                problem = r?.optString("problem") ?: "",
                detail = r?.optString("detail") ?: "",
            ),
        )
    }

    /**
     * The mesh /48 from a derived overlay address.
     *
     * Addresses are `fd<40 bits from the network key>:<80 bits from the device
     * key>`, so the first three groups are the prefix.
     */
    fun prefixOf(overlay: String): String {
        val groups = overlay.split(":")
        return if (groups.size >= 3) "${groups[0]}:${groups[1]}:${groups[2]}::" else overlay
    }
}

fun humanBytes(n: Long): String = when {
    n < 1024 -> "$n B"
    n < 1024 * 1024 -> "%.1f KB".format(n / 1024.0)
    else -> "%.1f MB".format(n / (1024.0 * 1024.0))
}

fun shortDuration(seconds: Long): String = when {
    seconds < 60 -> "${seconds}s"
    seconds < 3600 -> "${seconds / 60}m"
    else -> "${seconds / 3600}h"
}
