package xyz.vpavlin.shrooms

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Test

/** The tasks list on the phone: the same names, groups and order as the Basecamp board (ADR-047). */
class AgentTasksTest {
    private fun task(id: String, state: String, from: String = "laptop.default (laptop/shrooms)", at: Long = 0,
                     acked: Boolean = false, stalled: Boolean = false, asked: String = "", request: String = "",
                     latest: String = "") =
        AgentTask(id, id.substringBefore(':'), from, state, asked, request, latest, at, acked, stalled)

    // As the agent serves it (GET /v1/tasks), taken from a live task.
    @Test fun aTaskIsReadAsTheAgentServesIt() {
        val t = AgentTasks.parse(JSONObject("""{
            "id":"jimmy:cli-20261009T183346-04a4f81dbc3a0c9e",
            "status":{"state":"TASK_STATE_INPUT_REQUIRED","timestamp":"2026-10-10T09:00:00Z",
                      "message":{"role":"ROLE_AGENT","parts":[{"text":"Pushed dcb0af9"}]}},
            "history":[{"role":"ROLE_USER","parts":[{"text":"From laptop/shrooms. Vaclav wants…\n\nmore"}]}],
            "metadata":{"shrooms/session":"jimmy","shrooms/from":"laptop.default (laptop/shrooms)",
                        "shrooms/title":"PR: tasks on the Shrooms Agents board","shrooms/stalled":false}}"""))
        assertEquals("jimmy", t.session)
        assertEquals("input-required", t.state)
        assertEquals("cli-20261009T183346-04a4f81dbc3a0c9e", t.messageId)
        assertEquals("PR: tasks on the Shrooms Agents board", AgentTasks.title(t))
        assertEquals("Pushed dcb0af9", t.latest)
        assertEquals(AgentClient.parseTime("2026-10-10T09:00:00Z"), t.at)
    }

    // The asker's title, then the request's first line, then what it last said, then the id.
    @Test fun aTaskIsNamedByWhatWasAsked() {
        assertEquals("named", AgentTasks.title(task("a:1", "working", asked = "named", request = "From X")))
        assertEquals("From X: the ask", AgentTasks.title(task("a:1", "working", request = "\n  From X:   the ask\n\nmore")))
        assertEquals("last word", AgentTasks.title(task("a:1", "working", latest = "last word")))
        assertEquals("a:1", AgentTasks.title(task("a:1", "working")))
    }

    // Needs you wins over stalled; acknowledged tasks are not listed.
    @Test fun tasksAreGroupedByWhatAPersonHasToDo() {
        assertEquals(AgentTasks.NEEDS_YOU, AgentTasks.group(task("a:1", "input-required", stalled = true)))
        assertEquals(AgentTasks.STALLED, AgentTasks.group(task("a:2", "working", stalled = true)))
        assertEquals(AgentTasks.WORKING, AgentTasks.group(task("a:3", "submitted")))
        assertEquals(AgentTasks.UNACKED, AgentTasks.group(task("a:4", "failed")))
        assertEquals(AgentTasks.DONE, AgentTasks.group(task("a:5", "completed", acked = true)))
    }

    @Test fun rowsComeInTheOrderAPersonNeedsThem() {
        val h = AgentHost("pi5", "office", "fd00::2", emptyList())
        val rows = AgentTasks.rows(listOf(h to listOf(
            task("a:done", "completed", at = 1, acked = true),
            task("a:unacked", "completed", at = 2),
            task("a:stalled", "working", at = 3, stalled = true),
            task("a:work-late", "working", at = 9),
            task("a:work-early", "working", at = 4),
            task("a:needs", "input-required", at = 5),
        )))
        assertEquals(listOf("a:needs", "a:work-early", "a:work-late", "a:stalled", "a:unacked"), rows.map { it.task.id })
        assertEquals("pi5", rows[0].host)
    }

    // The session the asking agent named; a cage as a flag; a bare device as itself.
    @Test fun theAskerIsTheSessionThatAsked() {
        assertEquals("shrooms" to false, AgentTasks.asker("laptop.default (laptop/shrooms)"))
        assertEquals("shrooms" to true, AgentTasks.asker("laptop (laptop/shrooms, in a cage)"))
        assertEquals("pi5.office" to false, AgentTasks.asker("pi5.office"))
        assertEquals("?" to false, AgentTasks.asker(""))
    }

    @Test fun anAgeSaysWhatItMeasures() {
        val now = 10_000_000L
        assertEquals("quiet 2h", AgentTasks.age(AgentTasks.WORKING, now - 2 * 3_600_000, now))
        assertEquals("done 3m", AgentTasks.age(AgentTasks.UNACKED, now - 3 * 60_000, now))
        assertEquals("", AgentTasks.age(AgentTasks.WORKING, 0, now))
    }

    // The jump lands where the task arrived, not on a later follow-up.
    @Test fun theArrivalIsTheHitThatStartsTheTask() {
        val id = "jimmy:cli-1"
        val hits = listOf(
            Found(4698, 0, "user", "[shrooms task $id — more from laptop.default (laptop/shrooms)] live pass", ""),
            Found(3920, 0, "user", "[shrooms task $id from laptop.default (laptop/shrooms)] From laptop", ""),
        )
        assertEquals(3920L, AgentTasks.arrival(hits, id)?.seq)
        assertEquals(4698L, AgentTasks.arrival(hits.take(1), id)?.seq)
        assertEquals(null, AgentTasks.arrival(emptyList(), id))
    }

    // The same agent reached by name and by an address lists its tasks twice; the phone lists them once.
    @Test fun aTaskReachedTwiceIsListedOnce() {
        val t = task("shrooms:m1", "working")
        val rows = AgentTasks.rows(listOf(
            AgentHost("laptop", "office", "fd00::1", emptyList()) to listOf(t),
            AgentHost("198", "office", "198.19.0.1", emptyList()) to listOf(t),
        ))
        assertEquals(1, rows.size)
    }

    // A queued task has not arrived: it says so instead of an age.
    @Test fun aQueuedTaskSaysSo() {
        val t = AgentTasks.parse(JSONObject("""{"id":"s:1","status":{"state":"TASK_STATE_SUBMITTED"},
            "metadata":{"shrooms/session":"s","shrooms/queued":true}}"""))
        assertEquals(true, t.queued)
        assertEquals("queued", AgentTasks.age(AgentTasks.group(t), 5, 10, t.queued))
    }
}
