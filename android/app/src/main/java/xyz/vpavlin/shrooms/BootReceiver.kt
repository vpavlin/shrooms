package xyz.vpavlin.shrooms

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.util.Log
import mobile.Mobile

/**
 * Bring the tunnel up after a reboot or an app update.
 *
 * A mesh you have to remember to switch on is off when you need it, and an
 * update currently leaves it off until someone opens the app.
 *
 * This works only when VPN consent has already been given — VpnService.prepare
 * returns null — because consent needs a visible activity and there is not one
 * here. That is the right behaviour: the first connection is a deliberate act,
 * and every one after it is not.
 *
 * Android's own answer to the same problem is Always-on VPN, which is stronger
 * (it survives the app being killed and blocks traffic while the tunnel is
 * down). The app points at that setting; this covers the case where it is off.
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (intent.action) {
            Intent.ACTION_BOOT_COMPLETED,
            Intent.ACTION_MY_PACKAGE_REPLACED,
            "android.intent.action.QUICKBOOT_POWERON" -> {}
            else -> return
        }

        val dir = context.filesDir.absolutePath
        if (!Mobile.configured(dir)) return

        // Not prepared means the user has never allowed a VPN, or revoked it.
        // Starting anyway would throw; the app will ask next time it is opened.
        if (VpnService.prepare(context) != null) {
            Log.i("shrooms", "not starting on boot: VPN consent not granted")
            return
        }

        Log.i("shrooms", "starting on ${intent.action}")
        context.startForegroundService(
            Intent(context, MeshVpnService::class.java).setAction(MeshVpnService.ACTION_CONNECT)
        )
    }
}
