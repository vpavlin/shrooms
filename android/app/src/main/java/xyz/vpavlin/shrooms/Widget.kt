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

    companion object {

        /**
         * Redraw every widget from the current state.
         *
         * Called by the service as the session changes rather than left to the
         * system's update period, which has a half-hour floor — a widget that
         * says "not connected" for twenty minutes after you connected is worse
         * than no widget.
         */
        fun refresh(ctx: Context) {
            val mgr = runCatching { AppWidgetManager.getInstance(ctx) }.getOrNull() ?: return
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
            //
            // Guarded, because this can run in a process the launcher woke for
            // a broadcast, before anything has loaded the native library. A
            // throw here is not a blank line on the widget: it is the whole
            // widget failing to appear, with a message that names nothing.
            val running = runCatching { Mobile.running() }.getOrDefault(false)
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
            views.setOnClickPendingIntent(R.id.widget_action, actionPending(ctx, running))
            views.setOnClickPendingIntent(R.id.widget_name, openPending(ctx))
            views.setOnClickPendingIntent(R.id.widget_peers, openPending(ctx))
            return views
        }

        /**
         * What the action line does, decided while drawing it.
         *
         * Aimed at the service rather than back at this receiver. The receiver
         * has to be exported for the system to deliver APPWIDGET_UPDATE, and a
         * custom action on an exported receiver is something any other app can
         * send; the service is not exported, so a PendingIntent to it is
         * usable by whoever holds it and by nobody else.
         *
         * VpnService.prepare needs an Activity to show its consent dialog and
         * a widget has none, so the first connection on a device that has
         * never granted it opens the app instead of failing silently. Every
         * connection after that happens without leaving the home screen, which
         * is the point of having this.
         */
        private fun actionPending(ctx: Context, running: Boolean): PendingIntent {
            val flags = PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT
            val ready = runCatching {
                VpnService.prepare(ctx) == null && Mobile.configured(ctx.filesDir.absolutePath)
            }.getOrDefault(false)

            return when {
                running -> PendingIntent.getService(
                    ctx, 1,
                    Intent(ctx, MeshVpnService::class.java)
                        .setAction(MeshVpnService.ACTION_DISCONNECT),
                    flags,
                )
                ready -> PendingIntent.getForegroundService(
                    ctx, 2,
                    Intent(ctx, MeshVpnService::class.java)
                        .setAction(MeshVpnService.ACTION_CONNECT),
                    flags,
                )
                else -> openPending(ctx)
            }
        }

        private fun openPending(ctx: Context): PendingIntent =
            PendingIntent.getActivity(
                ctx, 3,
                Intent(ctx, MainActivity::class.java),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
    }
}
