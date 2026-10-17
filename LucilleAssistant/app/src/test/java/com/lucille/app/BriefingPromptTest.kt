package com.lucille.app

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class BriefingPromptTest {
    @Test
    fun copyForAnAiIsAFormalPrompt() {
        val text = briefingText(Briefing("b1", "Game Mode deals", "LA", "Deals were fake.", "Wire them to /api/deals.", listOf("GameMode.kt", " ")))
        var last = -1
        // Same headings, same order as formalprompt.go on the PC.
        for (h in listOf("WHO:", "THE JOB:", "BACKGROUND:", "READ FIRST:", "HOW TO WORK:", "DONE WHEN:", "REPORT BACK:")) {
            val i = text.indexOf(h)
            assertTrue("$h missing or out of order", i > last)
            last = i
        }
        assertTrue(text.contains("\"Game Mode deals (LA)\""))
        assertTrue(text.contains("Deals were fake.\n\nWire them to /api/deals."))
        assertTrue(text.contains("- GameMode.kt") && !text.contains("- \n"))
    }

    @Test
    fun emptySectionsAreLeftOut() {
        val text = briefingText(Briefing("b2", "T", "", "", "", emptyList()))
        assertFalse(text.contains("BACKGROUND:"))
        assertFalse(text.contains("READ FIRST:"))
    }
}
