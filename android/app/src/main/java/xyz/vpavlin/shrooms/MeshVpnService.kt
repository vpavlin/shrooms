package xyz.vpavlin.shrooms

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.VpnService
import android.os.Build
import android.os.SystemClock
import android.system.OsConstants
import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import mobile.Mobile

/**
 * Holds the tunnel. Everything mesh-related happens in Go; this class exists
 * because three things are only reachable from Android: the TUN descriptor,
 * socket protection, and staying alive in the background.
 */
class MeshVpnService : VpnService() {

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var tunnel: android.os.ParcelFileDescriptor? = null

    // Watches the underlying (non-VPN) network so the DNS forwarder can be told
    // when its resolvers change. See watchNetworks.
    private var netCallback: ConnectivityManager.NetworkCallback? = null

    companion object {
        const val ACTION_CONNECT = "xyz.vpavlin.shrooms.CONNECT"
        const val ACTION_DISCONNECT = "xyz.vpavlin.shrooms.DISCONNECT"

        /**
         * Rebuild the tunnel from the config, in one ordered step.
         *
         * Not a disconnect followed by a connect from the caller: those are two
         * intents and the second can be handled before the first has finished,
         * which left the service stopped and the user looking at a Connect
         * button they had not asked for.
         */
        const val ACTION_RECONNECT = "xyz.vpavlin.shrooms.RECONNECT"

        /** How long the rendezvous plane may be down before rebuilding it. */
        private const val STALL_BEFORE_RECONNECT = 5 * 60_000L

        /** And how long to leave it alone afterwards. */
        private const val REVIVE_COOLDOWN = 15 * 60_000L
        private const val CHANNEL = "mesh"
        private const val NOTIFICATION_ID = 1
        private const val TAG = "shrooms"

        /** MTU 1280: the IPv6 minimum, which no path may fragment below. */
        private const val MTU = 1280
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_DISCONNECT -> {
                stop()
                return START_NOT_STICKY
            }
            ACTION_RECONNECT -> {
                restart()
                return START_STICKY
            }
            else -> start()
        }
        // STICKY so Android brings the tunnel back if it kills us for memory.
        return START_STICKY
    }

    private fun start() {
        if (Mobile.running()) return

        val dir = filesDir.absolutePath
        if (!Mobile.configured(dir)) {
            MeshState.fail("not configured yet")
            stopSelf()
            return
        }

        startForeground(NOTIFICATION_ID, notification("Connecting…"))

        scope.launch { startInline() }
    }

    /** The body of start(), so a restart can run it in its own sequence. */
    private suspend fun startInline() {
        val dir = filesDir.absolutePath
        run {
            try {
                val overlay = Mobile.overlayAddress(dir)
                if (overlay.isEmpty()) throw IllegalStateException("no overlay address; config unreadable")

                // Route only the mesh prefix. Routing everything would make this
                // a full-tunnel VPN, which it is not — other traffic must keep
                // using the normal network.
                val prefix = overlay.substringBeforeLast(":").let { MeshState.prefixOf(overlay) }

                val builder = Builder()
                    .setSession("Shrooms")
                    .setMtu(MTU)
                    .setBlocking(false)

                // An address and routes for every mesh (ADR-015).
                //
                // Go decides what these are, because it is where the identities
                // and the address derivation live — and a mesh whose route is
                // missing looks like a mesh that silently does not work, with
                // nothing in the log to say so.
                var installed = 0
                runCatching {
                    val arr = org.json.JSONArray(Mobile.meshesJSON(dir))
                    for (i in 0 until arr.length()) {
                        val m = arr.getJSONObject(i)
                        val ov = m.optString("overlay")
                        val pfx = m.optString("prefix")
                        if (ov.isEmpty() || !pfx.contains("/")) continue
                        builder.addAddress(ov, 128)
                        builder.addRoute(pfx.substringBefore("/"), pfx.substringAfter("/").toInt())

                        // The synthetic IPv4 side, per mesh: browsers ask for A
                        // records, and each mesh owns its own block.
                        val v4 = m.optString("v4")
                        val v4b = m.optString("v4_block")
                        if (v4.isNotEmpty() && v4b.contains("/")) {
                            builder.addAddress(v4, 32)
                            builder.addRoute(v4b.substringBefore("/"), v4b.substringAfter("/").toInt())
                        }
                        installed++
                        Log.i(TAG, "mesh ${m.optString("label")} at $ov ($pfx), v4 $v4b")
                    }
                }.onFailure { Log.w(TAG, "could not read the mesh list: ${it.message}") }

                if (installed == 0) {
                    // The single-mesh fallback, for a config this build cannot
                    // enumerate. Better than a tunnel with no addresses at all.
                    builder.addAddress(overlay, 128)
                    builder.addRoute(prefix, 48)
                    val v4 = Mobile.overlayV4(dir)
                    val v4Range = Mobile.overlayV4Range()
                    if (v4.isNotEmpty() && v4Range.contains("/")) {
                        builder.addAddress(v4, 32)
                        builder.addRoute(v4Range.substringBefore("/"), v4Range.substringAfter("/").toInt())
                    }
                }


                // Let everything that is not the mesh bypass the tunnel.
                //
                // This is the trap that cost three builds and looked like a DNS
                // bug every time. A VpnService BLOCKS an address family it has
                // an address for unless traffic matches a route — and it blocks
                // families it has no address for outright. Ours is IPv6-only
                // and routes one /48, so every IPv4 packet on the device was
                // being dropped: name servers are usually IPv4, so it presented
                // as "DNS does not work" while the actual fault was that
                // nothing but the mesh could send anything at all.
                //
                // allowFamily lets each family bypass; the /48 route still
                // captures the mesh, because routes take precedence.
                builder.allowFamily(OsConstants.AF_INET)
                builder.allowFamily(OsConstants.AF_INET6)

                // Names. The resolver answers in the tun read path, before
                // WireGuard sees the packet — Android routes queries for a
                // VPN's DNS server through the VPN, so a socket bound to our
                // own address could never receive them.
                //
                // Only claimed when we can also forward: addDnsServer hands us
                // EVERY query the device makes, so a resolver that only knows
                // .mesh removes name resolution entirely.
                val upstream = underlyingDnsServers()
                val dnsAddr = Mobile.dnsAddress(dir)
                if (upstream.isNotEmpty() && dnsAddr.isNotEmpty()) {
                    // NOT our own address: one the interface holds is delivered
                    // locally and never reaches the tun, so nothing could answer.
                    builder.addDnsServer(dnsAddr)
                    builder.addSearchDomain(Mobile.dnsSuffix(dir))
                    Log.i(TAG, "resolver at $dnsAddr")
                    MeshState.names(dnsAddr)
                } else {
                    Log.w(TAG, "no upstream resolvers; leaving DNS alone")
                    MeshState.names("")
                    MeshState.log("WARN", "mesh names unavailable: could not read the network's resolvers")
                }

                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                    // Our own traffic must not be captured by our own tunnel.
                    builder.addDisallowedApplication(packageName)
                }

                val pfd = builder.establish()
                    ?: throw IllegalStateException("VpnService.establish() returned null — permission revoked?")
                tunnel = pfd

                // Go dups this, so closing it here later is safe.
                Mobile.start(pfd.fd.toLong(), dir, upstream, protector, logger)
                setUnderlying()
                watchNetworks()

                MeshState.connected(overlay)
                notify("Connected · $overlay")
                poll()
            } catch (t: Throwable) {
                Log.e(TAG, "start failed", t)
                // The class name matters as much as the message: a null message
                // on a bare exception otherwise reports nothing at all.
                val what = t.message.takeUnless { it.isNullOrBlank() } ?: t.javaClass.simpleName
                MeshState.fail(what)
                MeshState.log("ERROR", "start failed: $what")
                t.stackTrace.take(4).forEach { MeshState.log("ERROR", "  at $it") }
                stop()
            }
        }
    }

    /**
     * The resolvers of the network underneath the tunnel.
     *
     * Read before establish() would also work, but it must be the UNDERLYING
     * network rather than ours: querying our own resolver through itself is a
     * loop. Android has no split-DNS, so we receive every query the device
     * makes and must forward what is not ours — without these the phone loses
     * name resolution entirely the moment the VPN comes up.
     */
    private fun underlyingDnsServers(): String = try {
        val cm = getSystemService(ConnectivityManager::class.java)

        fun isUsable(net: Network): Boolean {
            val caps = cm?.getNetworkCapabilities(net) ?: return false
            return caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) &&
                caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
        }
        fun dnsOf(net: Network?): List<String> =
            net?.let { cm?.getLinkProperties(it)?.dnsServers }
                ?.mapNotNull { it.hostAddress } ?: emptyList()

        // The network actually carrying traffic comes first, and the others
        // only after it.
        //
        // Order matters more than it looks. The forwarder tries each resolver
        // in turn with a timeout, so an unreachable one at the head of the list
        // costs that timeout on every single query — and Android reacts to a
        // slow resolver by dropping it, taking .mesh names with it. A phone
        // holding wifi and mobile data at once yields both sets, and only one
        // of them is reachable.
        //
        // activeNetwork is consulted but never trusted: once our own VpnService
        // is up it IS the active network, and its resolver is us. Hence the
        // NOT_VPN check before using it, and the scan as a fallback.
        val active = cm?.activeNetwork?.takeIf { isUsable(it) }
        val ordered = buildList {
            addAll(dnsOf(active))
            (cm?.allNetworks ?: emptyArray())
                .filter { it != active && isUsable(it) }
                .forEach { addAll(dnsOf(it)) }
        }.distinct()

        Log.i(TAG, "upstream resolvers: $ordered (active=${active != null})")
        ordered.joinToString(",")
    } catch (t: Throwable) {
        // Needs ACCESS_NETWORK_STATE, and a SecurityException here must not
        // take the tunnel down — it only means no mesh names.
        Log.w(TAG, "could not read upstream resolvers: ${t.message}")
        ""
    }

    /**
     * Forwards to VpnService.protect.
     *
     * Without this the sockets carrying rendezvous and disco traffic would be
     * routed into the tunnel that depends on them, and nothing would work with
     * no error anywhere. Go refuses to continue if this returns false.
     */
    private val protector = object : mobile.Protector {
        override fun protect(fd: Long): Boolean {
            // Qualified deliberately: an unqualified protect() here reads as a
            // call to this very method, and the day Kotlin resolves it that way
            // it is an infinite recursion rather than a socket being protected.
            val ok = this@MeshVpnService.protect(fd.toInt())
            if (!ok) Log.e(TAG, "failed to protect socket $fd")
            return ok
        }
    }

    private val logger = object : mobile.Logger {
        override fun log(level: String, message: String) {
            when (level) {
                "ERROR" -> Log.e(TAG, message)
                "WARN" -> Log.w(TAG, message)
                else -> Log.i(TAG, message)
            }
            MeshState.log(level, message)
        }
    }

    /** Polls the Go status snapshot for the UI. Cheap: a map read and a marshal. */
    private fun poll() {
        scope.launch {
            var healthy = SystemClock.elapsedRealtime()
            var revived = 0L
            while (Mobile.running()) {
                MeshState.update(Mobile.statusJSON())
                val s = MeshState.snapshot.value
                if (s.connected) notify(s.notificationLine())

                // Watchdog on the rendezvous plane.
                //
                // When it stops, tunnels keep carrying traffic and nothing
                // else changes — announces simply stop arriving, every peer
                // ages out, and the screen fills with devices marked offline
                // that are all perfectly fine. It looks like the mesh died and
                // it is really the one plane that discovers it.
                //
                // Rebuilding the session is what fixed it by hand every time,
                // so do that rather than wait to be asked. The cooldown is
                // long because a reconnect is disruptive and a network that is
                // genuinely gone will not be fixed by another one.
                val now = SystemClock.elapsedRealtime()
                when {
                    !s.connected || s.rendezvous.ok -> healthy = now
                    now - healthy > STALL_BEFORE_RECONNECT &&
                        now - revived > REVIVE_COOLDOWN -> {
                        revived = now
                        Log.w(TAG, "rendezvous stalled: ${s.rendezvous.problem} — rebuilding")
                        MeshState.log("WARN", "rendezvous stalled, reconnecting")
                        restart()
                    }
                }
                delay(2000)
            }
        }
    }

    /**
     * Rebuild the tunnel after a config change, in order.
     *
     * One coroutine, because stop() and start() each launch their own: calling
     * them in sequence returned immediately from both and started the new
     * session while the old one was still being torn down, which left the
     * tunnel up and carrying nothing until the app was force-stopped.
     *
     * The rendezvous node stays running throughout. Stopping and starting it is
     * the part that does not survive repetition — the library keeps
     * process-global state — and there is nothing to gain from a few seconds of
     * silence.
     */
    private fun restart() {
        scope.launch {
            runCatching { Mobile.stopForRestart() }
                .onFailure { Log.e(TAG, "stop for restart", it) }
            runCatching { tunnel?.close() }
            tunnel = null
            MeshState.disconnected()
            startInline()
        }
    }

    private fun stop() {
        scope.launch {
            runCatching { Mobile.stop() }.onFailure { Log.e(TAG, "stop", it) }
            runCatching { tunnel?.close() }
            tunnel = null
            MeshState.disconnected()
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
        }
    }

    override fun onRevoke() {
        // The user turned the VPN off from system settings, or another app took
        // over. Not an error.
        Log.i(TAG, "VPN permission revoked")
        stop()
    }

    override fun onDestroy() {
        scope.cancel()
        unwatchNetworks()
        runCatching { Mobile.stop() }
        super.onDestroy()
    }

    /**
     * Keeps the DNS forwarder's upstream resolvers current as the phone moves
     * between networks.
     *
     * The list handed to Mobile.start belongs to the network the phone was on
     * at connect. After roaming — mobile data to wifi, typically — those
     * resolvers are unreachable, so every non-mesh query fails slowly against
     * each in turn. Android watches that, decides our resolver is dead, and
     * stops sending it anything at all. Mesh names disappear with it, because
     * the same resolver answers them: the symptom is ".mesh stopped resolving
     * after I changed network" while the tunnel itself is perfectly healthy.
     *
     * Requesting NOT_VPN explicitly, for the same reason underlyingDnsServers
     * does: once our own VpnService is up, it is a network too, and its
     * resolver is us.
     */
    private fun watchNetworks() {
        if (netCallback != null) return
        val cm = getSystemService(ConnectivityManager::class.java) ?: return

        val request = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()

        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(network: Network) = refresh("available")
            override fun onLost(network: Network) = refresh("lost")
            override fun onLinkPropertiesChanged(network: Network, props: LinkProperties) =
                refresh("link properties changed")

            private fun refresh(why: String) {
                // Tell the system which network the tunnel actually runs over.
                //
                // Without this a VpnService keeps whatever underlying network it
                // had when establish() was called: after roaming, Android still
                // believes the tunnel runs over the network you left. That
                // affects how it treats the VPN network — its capabilities, its
                // validation state, and its resolver — which is the shape of
                // "names work until I change network".
                //
                // The active non-VPN network is passed explicitly rather than
                // null, so the system is told rather than left to infer.
                setUnderlying()

                val servers = underlyingDnsServers()
                if (servers.isEmpty()) {
                    // Between networks. Mobile.setDNSServers would refuse this
                    // anyway; saying so here is what makes the log readable.
                    Log.i(TAG, "network $why, no resolvers yet; keeping the previous ones")
                    return
                }
                val applied = runCatching { Mobile.setDNSServers(servers) }.getOrDefault(false)
                Log.i(TAG, "network $why, resolvers now $servers (applied=$applied)")
            }
        }

        runCatching { cm.registerNetworkCallback(request, cb) }
            .onSuccess { netCallback = cb }
            .onFailure { Log.w(TAG, "could not watch networks: ${it.message}") }
    }

    /**
     * Points the VPN at the network currently carrying its traffic.
     *
     * Called on every change and once at connect. Passing null would mean "use
     * the system default", which is also correct but tells the system less; an
     * explicit network is what makes accounting and capability reporting right.
     */
    private fun setUnderlying() {
        val cm = getSystemService(ConnectivityManager::class.java) ?: return
        val active = cm.activeNetwork?.takeIf { net ->
            val caps = cm.getNetworkCapabilities(net)
            caps != null &&
                caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET) &&
                caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
        }
        runCatching { setUnderlyingNetworks(active?.let { arrayOf(it) }) }
            .onFailure { Log.w(TAG, "could not set underlying networks: ${it.message}") }
    }

    private fun unwatchNetworks() {
        val cb = netCallback ?: return
        netCallback = null
        val cm = getSystemService(ConnectivityManager::class.java)
        runCatching { cm?.unregisterNetworkCallback(cb) }
    }

    // --- notification -------------------------------------------------------

    private fun notification(text: String): Notification {
        val mgr = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (mgr.getNotificationChannel(CHANNEL) == null) {
            mgr.createNotificationChannel(
                NotificationChannel(CHANNEL, "Mesh", NotificationManager.IMPORTANCE_LOW).apply {
                    description = "Shown while the mesh is connected"
                    setShowBadge(false)
                }
            )
        }

        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE
        )
        val disconnect = PendingIntent.getService(
            this, 1, Intent(this, MeshVpnService::class.java).setAction(ACTION_DISCONNECT),
            PendingIntent.FLAG_IMMUTABLE
        )

        return Notification.Builder(this, CHANNEL)
            .setContentTitle("Shrooms")
            .setContentText(text)
            .setSmallIcon(R.drawable.ic_mesh)
            .setContentIntent(open)
            .addAction(Notification.Action.Builder(null, "Disconnect", disconnect).build())
            .setOngoing(true)
            .build()
    }

    private fun notify(text: String) {
        val mgr = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        mgr.notify(NOTIFICATION_ID, notification(text))
    }
}
