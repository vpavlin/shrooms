package xyz.vpavlin.shrooms

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import org.json.JSONObject

/**
 * The app's view of the mesh, parsed from the Go status snapshot.
 *
 * Deliberately a mirror of `status --json` rather than a set of bound types:
 * adding a field in Go must not be an API change on both sides. See ADR-016.
 */
data class MeshInfo(val label: String, val overlay: String, val prefix: String, val peers: Int)

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
    /** Which mesh this peer is on; empty on a single-mesh device (ADR-015). */
    val mesh: String = "",
    /**
     * What this peer says it publishes (ADR-023), empty unless it has been told
     * to announce. A claim about what it offers, not a report that anything is
     * listening — only the publishing device knows whether the port answers.
     */
    val services: List<String> = emptyList(),
    /**
     * What this peer says is listening on its own mesh address, as "name:port"
     * (ADR-026). Reached as <device>.mesh:<port> — no forwarder and no name of
     * its own, which is why it is not in [services].
     *
     * A different kind of claim from a service, and shown as one: a service was
     * declared by somebody who meant it, a bound port is whatever happened to be
     * listening when the peer last looked.
     */
    val bound: List<String> = emptyList(),
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

data class Dns(
    val intercepted: Long = 0,
    val interceptFailed: Long = 0,
    val missed: Long = 0,
    val nxdomain: Long = 0,
    val nodataA: Long = 0,
    val nodataOther: Long = 0,
    val queries: Long = 0,
    val answers: Long = 0,
    val refused: Long = 0,
    val forwarded: Long = 0,
    val forwardFailed: Long = 0,
) {
    /** One line for the UI, saying which layer is doing something. */
    fun summary(): String {
        var s = "arrived $intercepted · answered $answers · refused $refused · forwarded $forwarded"
        if (forwardFailed > 0) s += " · fwd-fail $forwardFailed"
        // Queries aimed at the resolver in a form it does not answer, which in
        // practice means DNS over TCP. An app that resolves nothing while
        // another resolves everything is usually this.
        if (missed > 0) s += " · unanswerable $missed"
        // The two outcomes that look like success from the intercept's side
        // and like failure from the browser's.
        if (nxdomain > 0) s += " · unknown-name $nxdomain"
        if (nodataA > 0) s += " · ipv4-only $nodataA"
        if (nodataOther > 0) s += " · other-type $nodataOther"
        return s
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
    /**
     * Where this device tells peers to reach it.
     *
     * Empty is the interesting case and it is invisible everywhere else: a
     * phone that cannot list its own addresses — Android restricts that for
     * untrusted apps — announces nothing and looks perfectly healthy while no
     * peer can dial it. Null means a core too old to report it, which is a
     * different thing from none.
     */
    val announced: List<String>? = null,
    /** One entry per mesh, present only when this device has more than one. */
    val meshes: List<MeshInfo> = emptyList(),

    // Names is where the resolver was installed, or "" when it was not.
    //
    // State, not a log line. Whether mesh names work is decided once at
    // connect and then holds for the life of the tunnel, so a message about it
    // scrolls out of the log within seconds and the question "do names work?"
    // becomes unanswerable from the screen. It was unanswerable exactly when
    // it mattered.
    val names: String = "",

    // What each layer of DNS actually saw. See statusPayload.DNS in the Go
    // side: the point is to tell "nothing asked us" apart from "we answered
    // and it was ignored", which are indistinguishable from the outside.
    val dns: Dns = Dns(),
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

    /** Records where the resolver was installed, or "" if it could not be. */
    fun names(addr: String) {
        _snapshot.value = _snapshot.value.copy(names = addr)
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
            names = _snapshot.value.names,
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
                val svc = mutableListOf<String>()
                p.optJSONArray("services")?.let { a ->
                    for (j in 0 until a.length()) svc += a.optString(j)
                }
                val bnd = mutableListOf<String>()
                p.optJSONArray("bound")?.let { a ->
                    for (j in 0 until a.length()) bnd += a.optString(j)
                }
                peers += Peer(
                    services = svc,
                    bound = bnd,
                    mesh = p.optString("mesh"),
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
        // has() rather than optJSONArray alone: absent and empty are different
        // answers, and conflating them reports "no addresses" for a core that
        // simply predates the field.
        val announced = if (o.has("announced")) {
            o.optJSONArray("announced").let { arr ->
                (0 until (arr?.length() ?: 0)).map { arr!!.getString(it) }
            }
        } else null
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
            announced = announced,
            meshes = o.optJSONArray("meshes")?.let { arr ->
                (0 until arr.length()).map { i ->
                    val m = arr.getJSONObject(i)
                    MeshInfo(
                        label = m.optString("label"),
                        overlay = m.optString("overlay"),
                        prefix = m.optString("prefix"),
                        peers = m.optInt("peers"),
                    )
                }
            } ?: emptyList(),
            dns = o.optJSONObject("dns").let { d ->
                Dns(
                    intercepted = d?.optLong("intercepted") ?: 0,
                    interceptFailed = d?.optLong("intercept_failed") ?: 0,
                    missed = d?.optLong("missed") ?: 0,
                    nxdomain = d?.optLong("nxdomain") ?: 0,
                    nodataA = d?.optLong("nodata_a") ?: 0,
                    nodataOther = d?.optLong("nodata_other") ?: 0,
                    queries = d?.optLong("queries") ?: 0,
                    answers = d?.optLong("answers") ?: 0,
                    refused = d?.optLong("refused") ?: 0,
                    forwarded = d?.optLong("forwarded") ?: 0,
                    forwardFailed = d?.optLong("forward_failed") ?: 0,
                )
            },
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
