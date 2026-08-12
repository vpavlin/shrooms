package xyz.vpavlin.shrooms

import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.widget.RemoteViews
import mobile.Mobile

/**
 * The mesh on a home screen.
 *
 * A mesh you have to open an app to see is one you stop looking at, and the two
 * questions worth answering — is it up, and how much of it can I actually reach
 * — fit in two lines. The third line is the only control that matters often
 * enough to earn a place here.
 *
 * Drawn with RemoteViews rather than Glance. A widget is rendered by the
 * launcher's process from a description we hand over, so only a small set of
 * views is available; Glance would be nicer to write and costs a Compose
 * runtime and a dependency for four lines of text.
 */
class ShroomsWidget : AppWidgetProvider() {

    override fun onUpdate(ctx: Context, mgr: AppWidgetManager, ids: IntArray) {
        for (id in ids) mgr.updateAppWidget(id, render(ctx))
    }

    override fun onReceive(ctx: Context, intent: Intent) {
        super.onReceive(ctx, intent)
        if (intent.action == ACTION_TOGGLE) toggle(ctx)
    }

    /**
     * Connect, or disconnect, from a tap on the widget.
     *
     * VpnService.prepare needs an Activity to show its consent dialog, and a
     * widget has none — so the first connection on a device that has never
     * granted it opens the app instead of failing silently. Every connection
     * after that happens without leaving the home screen, which is the point.
     */
    private fun toggle(ctx: Context) {
        if (Mobile.running()) {
            ctx.startService(
                Intent(ctx, MeshVpnService::class.java).setAction(MeshVpnService.ACTION_DISCONNECT)
            )
            return
        }
        if (VpnService.prepare(ctx) != null || !Mobile.configured(ctx.filesDir.absolutePath)) {
            ctx.startActivity(
                Intent(ctx, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            )
            return
        }
        ctx.startForegroundService(
            Intent(ctx, MeshVpnService::class.java).setAction(MeshVpnService.ACTION_CONNECT)
        )
    }

    companion object {
        private const val ACTION_TOGGLE = "xyz.vpavlin.shrooms.WIDGET_TOGGLE"

        /**
         * Redraw every widget from the current state.
         *
         * Called by the service as the session changes rather than left to the
         * system's update period, which has a half-hour floor — a widget that
         * says "not connected" for twenty minutes after you connected is worse
         * than no widget.
         */
        fun refresh(ctx: Context) {
            val mgr = AppWidgetManager.getInstance(ctx) ?: return
            val ids = mgr.getAppWidgetIds(ComponentName(ctx, ShroomsWidget::class.java))
            if (ids.isEmpty()) return
            val views = render(ctx)
            for (id in ids) mgr.updateAppWidget(id, views)
        }

        private fun render(ctx: Context): RemoteViews {
            val views = RemoteViews(ctx.packageName, R.layout.widget)

            // Read the snapshot the service already maintains rather than
            // parsing status again. The widget provider runs in the app's own
            // process, so this is the same state the app itself shows — a
            // second parser here would be a second thing to keep in step.
            val running = Mobile.running()
            val snap = if (running) MeshState.snapshot.value else null

            views.setImageViewResource(
                R.id.widget_dot,
                if (snap?.connected == true) R.drawable.widget_dot_on else R.drawable.widget_dot_off,
            )
            views.setTextViewText(
                R.id.widget_name,
                snap?.name?.takeUnless { it.isEmpty() } ?: ctx.getString(R.string.app_name),
            )
            views.setTextViewText(
                R.id.widget_peers,
                when {
                    snap == null -> ctx.getString(R.string.widget_disconnected)
                    else -> snap.notificationLine()
                },
            )
            views.setTextViewText(
                R.id.widget_action,
                ctx.getString(
                    if (running) R.string.widget_disconnect else R.string.widget_connect
                ),
            )

            // The action line toggles; everything else opens the app, because
            // the graph and the roster are what you want when the summary is
            // not enough.
            views.setOnClickPendingIntent(R.id.widget_action, togglePending(ctx))
            views.setOnClickPendingIntent(R.id.widget_name, openPending(ctx))
            views.setOnClickPendingIntent(R.id.widget_peers, openPending(ctx))
            return views
        }

        private fun togglePending(ctx: Context): PendingIntent =
            PendingIntent.getBroadcast(
                ctx, 1,
                Intent(ctx, ShroomsWidget::class.java).setAction(ACTION_TOGGLE),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )

        private fun openPending(ctx: Context): PendingIntent =
            PendingIntent.getActivity(
                ctx, 2,
                Intent(ctx, MainActivity::class.java),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
    }
}
