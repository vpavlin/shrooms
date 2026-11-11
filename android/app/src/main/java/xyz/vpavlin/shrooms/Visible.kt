package xyz.vpavlin.shrooms

import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.currentStateAsState

/**
 * Whether this screen is on screen now: started, not merely alive.
 *
 * The Agents app's notification watcher is a foreground service, so its
 * process outlives the screen: after Home the activity is stopped, not
 * destroyed, and every LaunchedEffect loop kept polling, streaming and saving
 * in the pocket. On 2026-11-09 that drained a phone by 60% in two hours. A
 * loop keyed on this ends when the screen goes and starts again when it is
 * back; background work is the watcher's alone (AgentWatch).
 */
@Composable
fun screenVisible(): Boolean {
    val state by LocalLifecycleOwner.current.lifecycle.currentStateAsState()
    return state.isAtLeast(Lifecycle.State.STARTED)
}
