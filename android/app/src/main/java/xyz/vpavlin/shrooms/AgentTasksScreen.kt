package xyz.vpavlin.shrooms

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
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

/** The machines worth asking: one per address, and only those the list has heard from lately. */
fun taskTargets(hosts: List<AgentHost>, now: Long = System.currentTimeMillis()): List<AgentHost> =
    hosts.filter { HostCache.reachable(it, now) }.distinctBy { it.address }

/**
 * The tasks between agents, grouped by what a person has to do about them,
 * with ACK where a result waits to be seen and a tap that opens the worker's
 * session where the task arrived. The phone's side of the Basecamp board's
 * panel; the board itself, a wide-screen layout, stays on the desktop.
 */
@Composable
fun TasksScreen(hosts: List<AgentHost>, initial: List<TaskRow>?, onRows: (List<TaskRow>) -> Unit,
                onOpen: (TaskRow) -> Unit, onBack: () -> Unit) {
    // What each machine said last, by address: seeded with the rows the
    // Agents screen already had (or the phone kept), so the list is there at
    // once; each machine's answer replaces its part as soon as it comes,
    // without waiting for the slowest one.
    var perHost by remember {
        mutableStateOf(initial.orEmpty().groupBy { it.address }.mapValues { (_, rs) ->
            AgentHost(rs[0].host, rs[0].mesh, rs[0].address, emptyList()) to rs.map { it.task }
        })
    }
    var answered by remember { mutableStateOf(initial != null) }
    var localAcked by remember { mutableStateOf(setOf<String>()) }
    // Tasks answered here, as working again until their agents say so.
    var localStates by remember { mutableStateOf(mapOf<String, String>()) }
    fun current() = AgentTasks.rows(AgentTasks.withLocal(perHost.values.toList(), localAcked, localStates))
    val rows = current()
    var said by remember { mutableStateOf("") }
    var round by remember { mutableStateOf(0) }
    val scope = rememberCoroutineScope()
    val targets = remember(hosts) { taskTargets(hosts) }
    LaunchedEffect(targets, round) {
        while (isActive) {
            coroutineScope {
                for (h in targets) launch {
                    val ts = withContext(Dispatchers.IO) { runCatching { AgentClient(h.address).tasks() }.getOrNull() }
                    if (ts != null) {
                        perHost = perHost + (h.address to (h to ts))
                        answered = true
                    }
                }
            }
            onRows(current())
            delay(10_000)
        }
    }
    // Marks tasks acknowledged here, at once: the agents' answers come back
    // the same, and waiting for every machine before the row moved made ACK
    // look like it did nothing (2026-10-10).
    // Kept as a set over whatever the agents say, so a refresh that started
    // before the ACK cannot bring the row back for a round.
    fun markAcked(ids: Set<String>, on: Boolean) {
        localAcked = if (on) localAcked + ids else localAcked - ids
        onRows(current())
    }
    fun ack(rs: List<TaskRow>) {
        if (rs.isEmpty()) return
        markAcked(rs.map { it.task.id }.toSet(), true)
        scope.launch {
            // All at once: ACK ALL on a long list one by one took a while.
            val failed = withContext(Dispatchers.IO) {
                coroutineScope {
                    rs.map { r -> async { runCatching { AgentClient(r.address).ack(r.task.id) }.exceptionOrNull()?.let { r to it } } }
                        .awaitAll().filterNotNull()
                }
            }
            // What an agent refused comes back, with why.
            if (failed.isNotEmpty()) markAcked(failed.map { it.first.task.id }.toSet(), false)
            said = when {
                failed.isEmpty() -> if (rs.size == 1) "acked ${AgentTasks.title(rs[0].task)}" else "acked ${rs.size}"
                else -> "could not ack ${failed.size}: ${failed[0].second.message}"
            }
        }
    }
    // What can be done about a task waiting on its asker (docs/a2a-tasks.md):
    // answer it here, remind the asking agent, or call it off.
    fun act(r: TaskRow, what: String, call: (AgentClient) -> Unit, done: String, after: () -> Unit = {}) {
        scope.launch {
            val err = withContext(Dispatchers.IO) { runCatching { call(AgentClient(r.address)) }.exceptionOrNull() }
            said = if (err == null) done else "could not $what: ${err.message}"
            if (err == null) after()
        }
    }
    fun answer(r: TaskRow, text: String) = act(r, "answer", { it.answer(r.task.id, text) },
        "answered ${AgentTasks.title(r.task)}") { localStates = localStates + (r.task.id to "working"); onRows(current()) }
    fun nudge(r: TaskRow) = act(r, "nudge", { it.nudge(r.task.id) }, "reminded ${AgentTasks.asker(r.task.from).first}")
    fun cancel(r: TaskRow) {
        // Gone at once, as an ACK is: called off from here, there is nothing left to see.
        markAcked(setOf(r.task.id), true)
        act(r, "cancel", { it.cancel(r.task.id) }, "cancelled ${AgentTasks.title(r.task)}")
    }
    var cancelling by remember { mutableStateOf<TaskRow?>(null) }
    cancelling?.let { r ->
        androidx.compose.material3.AlertDialog(
            onDismissRequest = { cancelling = null },
            title = { Text("Cancel this task?") },
            text = { Text("“${AgentTasks.title(r.task)}” is called off on ${r.host}. ${r.task.session} stops waiting for an answer.") },
            confirmButton = { androidx.compose.material3.TextButton(onClick = { cancelling = null; cancel(r) }) { Text("Cancel it", color = Palette.Rust) } },
            dismissButton = { androidx.compose.material3.TextButton(onClick = { cancelling = null }) { Text("Keep") } },
        )
    }
    // Finished tasks are folded into one line: the list is what needs you
    // and what is under way, not a pile of things already done.
    var showDone by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxSize().background(Palette.Void).padding(horizontal = 16.dp, vertical = 12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("‹", color = Palette.Phosphor, style = MaterialTheme.typography.titleLarge,
                modifier = Modifier.clickable(onClick = onBack).padding(end = 12.dp))
            Text("TASKS", style = MaterialTheme.typography.labelSmall, color = Palette.Phosphor)
        }
        if (said.isNotEmpty()) Text(said, style = MaterialTheme.typography.labelSmall, maxLines = 1, overflow = TextOverflow.Ellipsis,
            color = if (said.startsWith("could not")) Palette.Rust else Palette.Ash, modifier = Modifier.padding(top = 6.dp))
        val rs = rows.takeIf { it.isNotEmpty() || answered }
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
                        if (g == AgentTasks.UNACKED) Text(if (showDone) "hide" else "show", style = MaterialTheme.typography.labelSmall,
                            color = Palette.Phosphor, modifier = Modifier.clickable { showDone = !showDone }.padding(start = 14.dp))
                        Spacer(Modifier.weight(1f))
                        if (g == AgentTasks.UNACKED) Text("ACK ALL", style = MaterialTheme.typography.labelSmall,
                            color = Palette.Amber, modifier = Modifier.clickable { ack(inGroup) }.padding(start = 12.dp))
                    }
                }
                if (g == AgentTasks.UNACKED && !showDone) continue
                items(inGroup, key = { it.host + "/" + it.task.id }) { r ->
                    TaskRowView(r, now, onOpen = { onOpen(r) }, onAck = { ack(listOf(r)) },
                        onAnswer = { answer(r, it) }, onNudge = { nudge(r) }, onCancel = { cancelling = r })
                }
            }
        }
    }
}

@Composable
private fun TaskRowView(r: TaskRow, now: Long, onOpen: () -> Unit, onAck: () -> Unit,
                        onAnswer: (String) -> Unit, onNudge: () -> Unit, onCancel: () -> Unit) {
    val t = r.task
    val (asker, caged) = AgentTasks.asker(t.from)
    val waiting = r.group == AgentTasks.NEEDS_YOU || r.group == AgentTasks.BLOCKED
    val tone = when (r.group) {
        AgentTasks.NEEDS_YOU -> Palette.Amber
        AgentTasks.STALLED -> Palette.Rust
        AgentTasks.WORKING -> Palette.Phosphor
        else -> Palette.Ash
    }
    var answering by remember { mutableStateOf(false) }
    var reply by remember { mutableStateOf("") }
    Column(Modifier.fillMaxWidth()) {
        Row(verticalAlignment = Alignment.Top, modifier = Modifier.fillMaxWidth()) {
            Column(Modifier.weight(1f).clickable(onClick = onOpen)) {
                Text(AgentTasks.title(t), style = MaterialTheme.typography.bodyMedium, color = Palette.Bone,
                    maxLines = 2, overflow = TextOverflow.Ellipsis)
                val who = "$asker${if (caged) " (caged)" else ""}"
                val age = AgentTasks.age(r.group, t.at, now, t.queued)
                // Whom it waits on, said plainly: blocked waits on the agent that asked.
                Text(if (r.group == AgentTasks.BLOCKED) "${t.session} · ${r.host} waits on $who  $age"
                     else "$who → ${t.session} · ${r.host}  $age",
                    style = MaterialTheme.typography.labelSmall, color = tone, maxLines = 1, overflow = TextOverflow.Ellipsis)
                val latest = t.latest.replace(Regex("\\s+"), " ").trim()
                if (latest.isNotEmpty()) Text(if (waiting) "needs: $latest" else latest, style = MaterialTheme.typography.labelSmall,
                    color = if (waiting) Palette.Bone else Palette.Ash, maxLines = if (waiting) 3 else 2, overflow = TextOverflow.Ellipsis)
            }
            if (r.group == AgentTasks.UNACKED) {
                Spacer(Modifier.width(12.dp))
                Text("ACK", style = MaterialTheme.typography.labelSmall, color = Palette.Amber,
                    modifier = Modifier.clickable(onClick = onAck).padding(4.dp))
            }
        }
        if (waiting && !answering) Row(Modifier.padding(top = 4.dp), horizontalArrangement = Arrangement.spacedBy(16.dp)) {
            TaskAction("answer", Palette.Phosphor) { answering = true }
            if (r.group == AgentTasks.BLOCKED) TaskAction("nudge $asker", Palette.Ash, onNudge)
            TaskAction("cancel", Palette.Rust, onCancel)
        }
        if (answering) Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(top = 6.dp)) {
            androidx.compose.material3.OutlinedTextField(reply, { reply = it }, modifier = Modifier.weight(1f),
                placeholder = { Text("your answer, sent to ${t.session}", style = MaterialTheme.typography.labelSmall) },
                textStyle = MaterialTheme.typography.bodySmall, maxLines = 4)
            Spacer(Modifier.width(10.dp))
            Column {
                TaskAction("send", if (reply.isNotBlank()) Palette.Phosphor else Palette.Ash) {
                    if (reply.isNotBlank()) { onAnswer(reply.trim()); reply = ""; answering = false }
                }
                Spacer(Modifier.height(8.dp))
                TaskAction("close", Palette.Ash) { answering = false }
            }
        }
    }
}

@Composable
private fun TaskAction(text: String, colour: androidx.compose.ui.graphics.Color, onClick: () -> Unit) =
    Text(text, style = MaterialTheme.typography.labelSmall, color = colour,
        modifier = Modifier.clickable(onClick = onClick).padding(vertical = 4.dp))
