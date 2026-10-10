package xyz.vpavlin.shrooms

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
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
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Files between agents, as a session's owner sees them (ADR-048): who asked
 * to send it files and was refused, who it takes them from, and what it was
 * sent. Nobody may send until allowed here (or from a shell on its machine).
 */
object AgentFiles {
    private val ENTRY = Regex("""^[^/\s]+/[^/\s]+$""")

    /** What an allow entry looks like: MACHINE/SESSION, or MACHINE and a star for all of it. */
    fun valid(entry: String): Boolean = ENTRY.matches(entry.trim())

    /** The list with entry added once, or unchanged when it is there or not an entry. */
    fun allow(list: List<String>, entry: String): List<String> {
        val e = entry.trim()
        return if (!valid(e) || e in list) list else list + e
    }

    fun deny(list: List<String>, entry: String): List<String> = list.filter { it != entry }

    fun size(n: Long): String = when {
        n >= 1 shl 20 -> String.format("%.1f MB", n / 1048576.0)
        n >= 1 shl 10 -> String.format("%.1f kB", n / 1024.0)
        else -> "$n bytes"
    }
}

@Composable
fun FilesDialog(client: AgentClient, session: String, start: AgentSession?, onChanged: (AgentSession) -> Unit, onClose: () -> Unit) {
    var info by remember { mutableStateOf(start) }
    var files by remember { mutableStateOf<List<DroppedFile>?>(null) }
    var adding by remember { mutableStateOf("") }
    var said by remember { mutableStateOf("") }
    var round by remember { mutableStateOf(0) }
    val scope = rememberCoroutineScope()
    LaunchedEffect(round) {
        withContext(Dispatchers.IO) {
            runCatching { client.sessions().firstOrNull { it.name == session } }.getOrNull()?.let { info = it; onChanged(it) }
            files = runCatching { client.dropped(session) }.getOrNull() ?: files.orEmpty()
        }
    }
    fun change(what: String, call: () -> Unit) {
        scope.launch {
            withContext(Dispatchers.IO) { runCatching(call) }
                .onSuccess { said = ""; round++ }
                .onFailure { said = "could not $what: ${it.message}" }
        }
    }
    val allowed = info?.acceptFilesFrom.orEmpty()
    AlertDialog(
        onDismissRequest = onClose,
        title = { Text("Files · $session") },
        confirmButton = { TextButton(onClick = onClose) { Text("Done") } },
        text = {
            Column(Modifier.heightIn(max = 520.dp).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                if (said.isNotEmpty()) Text(said, color = Palette.Rust, style = MaterialTheme.typography.labelSmall)
                val asks = info?.fileRequests.orEmpty()
                if (asks.isNotEmpty()) {
                    Text("WANTS TO SEND FILES", style = MaterialTheme.typography.labelSmall, color = Palette.Amber)
                    for (a in asks) Row(verticalAlignment = Alignment.CenterVertically) {
                        Column(Modifier.weight(1f)) {
                            Text(a.from, style = MaterialTheme.typography.bodySmall, color = Palette.Bone)
                            if (a.name.isNotEmpty()) Text("tried ${a.name}" + if (a.size > 0) " (${AgentFiles.size(a.size)})" else "",
                                style = MaterialTheme.typography.labelSmall, color = Palette.Ash, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        }
                        Text("allow", color = Palette.Phosphor, style = MaterialTheme.typography.labelSmall,
                            modifier = Modifier.clickable { change("allow") { client.setAcceptFiles(session, AgentFiles.allow(allowed, a.from)) } }.padding(6.dp))
                        Text("ignore", color = Palette.Ash, style = MaterialTheme.typography.labelSmall,
                            modifier = Modifier.clickable { change("ignore") { client.ignoreFileRequest(session, a.from) } }.padding(6.dp))
                    }
                }
                Text("TAKES FILES FROM", style = MaterialTheme.typography.labelSmall, color = Palette.Ash, modifier = Modifier.padding(top = 8.dp))
                if (allowed.isEmpty()) Text("nobody yet", style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
                for (e in allowed) Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(e, style = MaterialTheme.typography.bodySmall, color = Palette.Bone, modifier = Modifier.weight(1f))
                    Text("×", color = Palette.Rust, modifier = Modifier.clickable {
                        change("remove") { client.setAcceptFiles(session, AgentFiles.deny(allowed, e)) }
                    }.padding(horizontal = 10.dp, vertical = 4.dp))
                }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    OutlinedTextField(adding, { adding = it }, modifier = Modifier.weight(1f), singleLine = true,
                        placeholder = { Text("machine/session or machine/*", style = MaterialTheme.typography.labelSmall) },
                        textStyle = MaterialTheme.typography.bodySmall)
                    Spacer(Modifier.width(8.dp))
                    Text("add", color = if (AgentFiles.valid(adding)) Palette.Phosphor else Palette.Ash,
                        style = MaterialTheme.typography.labelSmall, modifier = Modifier.clickable {
                            if (AgentFiles.valid(adding)) {
                                val e = adding.trim(); adding = ""
                                change("add") { client.setAcceptFiles(session, AgentFiles.allow(allowed, e)) }
                            }
                        }.padding(6.dp))
                }
                Text("RECEIVED", style = MaterialTheme.typography.labelSmall, color = Palette.Ash, modifier = Modifier.padding(top = 8.dp))
                val fs = files
                when {
                    fs == null -> Text("…", color = Palette.Ash)
                    fs.isEmpty() -> Text("nothing yet", style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
                    else -> for (f in fs) Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.fillMaxWidth()) {
                        Column(Modifier.weight(1f)) {
                            Text(f.name, style = MaterialTheme.typography.bodySmall, color = Palette.Bone, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            Text("from ${f.from} · ${AgentFiles.size(f.size)} · ${whenSaid(f.time)}",
                                style = MaterialTheme.typography.labelSmall, color = Palette.Ash, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        }
                        Text("delete", color = Palette.Rust, style = MaterialTheme.typography.labelSmall,
                            modifier = Modifier.clickable { change("delete") { client.removeDropped(session, f.from, f.name) } }.padding(6.dp))
                    }
                }
            }
        },
    )
}
