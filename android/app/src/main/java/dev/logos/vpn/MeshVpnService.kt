package dev.logos.vpn

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.net.ConnectivityManager
import android.net.VpnService
import android.os.Build
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

    companion object {
        const val ACTION_CONNECT = "dev.logos.vpn.CONNECT"
        const val ACTION_DISCONNECT = "dev.logos.vpn.DISCONNECT"
        private const val CHANNEL = "mesh"
        private const val NOTIFICATION_ID = 1
        private const val TAG = "logos-vpn"

        /** MTU 1280: the IPv6 minimum, which no path may fragment below. */
        private const val MTU = 1280
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_DISCONNECT -> {
                stop()
                return START_NOT_STICKY
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

        scope.launch {
            try {
                val overlay = Mobile.overlayAddress(dir)
                if (overlay.isEmpty()) throw IllegalStateException("no overlay address; config unreadable")

                // Route only the mesh prefix. Routing everything would make this
                // a full-tunnel VPN, which it is not — other traffic must keep
                // using the normal network.
                val prefix = overlay.substringBeforeLast(":").let { MeshState.prefixOf(overlay) }

                val builder = Builder()
                    .setSession("logos-vpn")
                    .addAddress(overlay, 128)
                    .addRoute(prefix, 48)
                    .setMtu(MTU)
                    .setBlocking(false)

                // Only take DNS over when we can forward what is not ours.
                //
                // VpnService has no split-DNS: addDnsServer sends us EVERY
                // query the device makes. Installing a resolver that can only
                // answer .mesh removes the phone's name resolution entirely —
                // no browsing, no app updates. Without upstreams, no mesh names
                // is the far smaller loss.
                val upstream = underlyingDnsServers()
                if (upstream.isNotEmpty()) {
                    builder.addDnsServer(overlay)
                    builder.addSearchDomain(Mobile.dnsSuffix(dir))
                } else {
                    Log.w(TAG, "no upstream resolvers; leaving DNS alone")
                    MeshState.log("WARN", "names unavailable: could not read the network's resolvers")
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
        // activeNetwork is still the physical one at this point; once our VPN
        // is up it becomes the VPN, whose resolver is us.
        val net = cm?.activeNetwork
        val props = net?.let { cm.getLinkProperties(it) }
        val servers = props?.dnsServers?.mapNotNull { it.hostAddress } ?: emptyList()
        Log.i(TAG, "upstream resolvers: $servers")
        servers.joinToString(",")
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
            while (Mobile.running()) {
                MeshState.update(Mobile.statusJSON())
                MeshState.snapshot.value.let { s ->
                    if (s.connected) notify(s.notificationLine())
                }
                delay(2000)
            }
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
        runCatching { Mobile.stop() }
        super.onDestroy()
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
            .setContentTitle("logos-vpn")
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
