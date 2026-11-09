package xyz.vpavlin.shrooms

import org.junit.Assert.assertEquals
import org.junit.Test

// The watcher asks each machine once a round, and less often one that keeps
// missing (AgentWatch.roundTargets).
class WatchRoundTest {
    private val laptopOffice = AgentHosts.Host("laptop", "office", "fdb0::1")
    private val laptopHome = AgentHosts.Host("laptop", "home", "fd7b::1")
    private val pi5 = AgentHosts.Host("pi5", "office", "fdb0::2")

    @Test fun aMachineOnTwoMeshesIsOneTarget() {
        val t = AgentWatch.roundTargets(listOf(laptopOffice, laptopHome, pi5), emptyMap(), emptyMap(), 1)
        assertEquals(2, t.size)
        assertEquals(2, t.first { it.first().name == "laptop" }.size)
    }

    @Test fun theAddressThatAnsweredLastIsTriedFirst() {
        val t = AgentWatch.roundTargets(listOf(laptopOffice, laptopHome), mapOf("laptop" to "fd7b::1"), emptyMap(), 1)
        assertEquals("fd7b::1", t.single().first().address)
    }

    @Test fun aMachineThatKeepsMissingIsAskedLessOften() {
        fun asked(misses: Int) = (1L..32L).count { r ->
            AgentWatch.roundTargets(listOf(pi5), emptyMap(), mapOf("pi5" to misses), r).isNotEmpty()
        }
        assertEquals(32, asked(0))
        assertEquals(32, asked(1))
        assertEquals(16, asked(2))
        assertEquals(2, asked(9)) // capped at every 16th round
    }
}
