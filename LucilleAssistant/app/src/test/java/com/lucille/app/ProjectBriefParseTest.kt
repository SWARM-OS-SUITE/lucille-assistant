package com.lucille.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ProjectBriefParseTest {
    private val md = """
# MASTER BUILD LIST

## SW: SWARM OS  (owner: x)
| ID | P | Item | Status | Notes / source |
|---|---|---|---|---|
| SW-01 | P1 | Boot the ISO | WIP | note |
| SW-02 | P0 | **Fix** the `crash` | TODO (user) | |
| SW-03 | P2 | Old thing | DONE, verified | |
| SW-04 | P2 | A maybe | IDEA | |

## LA: Lucille Assistant  (owner: SWARM AI)
| ID | P | Item | Status | Notes / source |
|---|---|---|---|---|
| LA-01 | P1 | Approval service | PARTIAL | |

---
## Idea inbox (append only; date and who)
- 2026-10-06 user: first idea
""".trimIndent()

    @Test
    fun readsOnlyTheRequestedSection() {
        val sw = parseTrackerRows(md, "SW")!!
        assertEquals(listOf("SW-01", "SW-02", "SW-03", "SW-04"), sw.map { it.id })
        assertEquals("Fix the crash", sw[1].item)
        assertEquals(listOf("LA-01"), parseTrackerRows(md, "LA")!!.map { it.id })
    }

    @Test
    fun noSectionMeansNullNotEmpty() {
        assertNull(parseTrackerRows(md, "ZZ"))
        assertNull(parseTrackerRows("", "SW"))
    }

    @Test
    fun openItemsAreSortedByPriorityAndExcludeDoneAndIdeas() {
        val t = summarizeTracker("MASTER_BUILD_LIST.md", "git", parseTrackerRows(md, "SW")!!)
        assertEquals(listOf("SW-02", "SW-01"), t.open.map { it.id })
        assertEquals(4, t.total)
        assertEquals(1, t.done)
        assertEquals("git", t.source)
    }

    @Test
    fun statusRules() {
        assertTrue(isOpenStatus("TODO (needs sudo)"))
        assertTrue(isOpenStatus("PARTIAL"))
        assertTrue(isOpenStatus("blocked"))
        assertFalse(isOpenStatus("DONE in code"))
        assertFalse(isOpenStatus("IDEA"))
        assertFalse(isOpenStatus(""))
    }
}
