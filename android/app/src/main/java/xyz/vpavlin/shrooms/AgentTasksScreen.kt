package xyz.vpavlin.shrooms

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Every machine's tasks, as one list (ADR-047): read from each agent, so it
 * is the tasks themselves and not a copy. A machine that does not answer
 * is left out of this round.
 */
suspend fun fetchTasks(hosts: List<AgentHost>): List<Pair<AgentHost, List<AgentTask>>> = coroutineScope {
    hosts.map { h ->
        async(Dispatchers.IO) { runCatching { h to AgentClient(h.address).tasks() }.getOrNull() }
    }.awaitAll().filterNotNull()
}

/**
 * The tasks between agents, grouped by what a person has to do about them,
 * with ACK where a result waits to be seen and a tap that opens the worker's
 * session where the task arrived. The phone's side of the Basecamp board's
 * panel; the board itself, a wide-screen layout, stays on the desktop.
 */
@Composable
fun TasksScreen(hosts: List<AgentHost>, onOpen: (TaskRow) -> Unit, onBack: () -> Unit) {
    var rows by remember { mutableStateOf<List<TaskRow>?>(null) }
    var said by remember { mutableStateOf("") }
    var round by remember { mutableStateOf(0) }
    val scope = rememberCoroutineScope()
    val targets = remember(hosts) { hosts.distinctBy { it.address } }
    LaunchedEffect(targets, round) {
        while (isActive) {
            rows = AgentTasks.rows(fetchTasks(targets))
            delay(10_000)
        }
    }
    fun ack(rs: List<TaskRow>) {
        scope.launch {
            val failed = withContext(Dispatchers.IO) {
                rs.mapNotNull { r -> runCatching { AgentClient(r.address).ack(r.task.id) }.exceptionOrNull()?.let { r to it } }
            }
            said = when {
                failed.isEmpty() -> if (rs.size == 1) "acked ${AgentTasks.title(rs[0].task)}" else "acked ${rs.size}"
                else -> "could not ack ${failed.size}: ${failed[0].second.message}"
            }
            round++
        }
    }
    Column(Modifier.fillMaxSize().background(Palette.Void).padding(horizontal = 16.dp, vertical = 12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("‹", color = Palette.Phosphor, style = MaterialTheme.typography.titleLarge,
                modifier = Modifier.clickable(onClick = onBack).padding(end = 12.dp))
            Text("TASKS", style = MaterialTheme.typography.labelSmall, color = Palette.Phosphor)
        }
        if (said.isNotEmpty()) Text(said, style = MaterialTheme.typography.labelSmall, maxLines = 1, overflow = TextOverflow.Ellipsis,
            color = if (said.startsWith("could not")) Palette.Rust else Palette.Ash, modifier = Modifier.padding(top = 6.dp))
        val rs = rows
        when {
            rs == null -> Text("asking the agents…", style = MaterialTheme.typography.bodySmall, color = Palette.Ash,
                modifier = Modifier.padding(top = 16.dp))
            rs.isEmpty() -> Text("Nothing open: no task waits on anyone, and every result has been seen.",
                style = MaterialTheme.typography.bodySmall, color = Palette.Ash, modifier = Modifier.padding(top = 16.dp))
        }
        val now = System.currentTimeMillis()
        LazyColumn(verticalArrangement = Arrangement.spacedBy(10.dp), modifier = Modifier.padding(top = 8.dp)) {
            for (g in AgentTasks.ORDER) {
                val inGroup = rs.orEmpty().filter { it.group == g }
                if (inGroup.isEmpty()) continue
                item(key = "g-$g") {
                    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth().padding(top = 12.dp)) {
                        Text("${AgentTasks.label(g).uppercase()}  ${inGroup.size}", style = MaterialTheme.typography.labelSmall,
                            color = if (g == AgentTasks.NEEDS_YOU) Palette.Amber else Palette.Ash)
                        Spacer(Modifier.weight(1f))
                        if (g == AgentTasks.UNACKED) Text("ACK ALL", style = MaterialTheme.typography.labelSmall,
                            color = Palette.Amber, modifier = Modifier.clickable { ack(inGroup) }.padding(start = 12.dp))
                    }
                }
                items(inGroup, key = { it.host + "/" + it.task.id }) { r -> TaskRowView(r, now, onOpen = { onOpen(r) }, onAck = { ack(listOf(r)) }) }
            }
        }
    }
}

@Composable
private fun TaskRowView(r: TaskRow, now: Long, onOpen: () -> Unit, onAck: () -> Unit) {
    val t = r.task
    val (asker, caged) = AgentTasks.asker(t.from)
    val tone = when (r.group) {
        AgentTasks.NEEDS_YOU -> Palette.Amber
        AgentTasks.STALLED -> Palette.Rust
        AgentTasks.WORKING -> Palette.Phosphor
        else -> Palette.Ash
    }
    Row(verticalAlignment = Alignment.Top, modifier = Modifier.fillMaxWidth()) {
        Column(Modifier.weight(1f).clickable(onClick = onOpen)) {
            Text(AgentTasks.title(t), style = MaterialTheme.typography.bodyMedium, color = Palette.Bone,
                maxLines = 2, overflow = TextOverflow.Ellipsis)
            Text("$asker${if (caged) " (caged)" else ""} → ${t.session} · ${r.host}  ${AgentTasks.age(r.group, t.at, now, t.queued)}",
                style = MaterialTheme.typography.labelSmall, color = tone, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (t.latest.isNotBlank()) Text(t.latest.replace(Regex("\\s+"), " ").trim(), style = MaterialTheme.typography.labelSmall,
                color = Palette.Ash, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        if (r.group == AgentTasks.UNACKED) {
            Spacer(Modifier.width(12.dp))
            Text("ACK", style = MaterialTheme.typography.labelSmall, color = Palette.Amber,
                modifier = Modifier.clickable(onClick = onAck).padding(4.dp))
        }
    }
}
