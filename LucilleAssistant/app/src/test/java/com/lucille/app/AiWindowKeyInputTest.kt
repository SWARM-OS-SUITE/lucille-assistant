package com.lucille.app

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

// org.json is stubbed in JVM unit tests (and the offline cache has no replacement), so only the
// pure key check is tested here; parseAiState is exercised against the real service in the emulator.
class AiWindowKeyInputTest {
    @Test fun keyInputIsCheckedBeforeItLeavesThePhone() {
        assertEquals("sk-or-v1-abcdef123456", cleanKeyInput("  sk-or-v1-abcdef123456\n"))
        assertNull(cleanKeyInput("short"))
        assertNull(cleanKeyInput("has a space inside 123"))
        assertNull(cleanKeyInput("sk-or-v1-abcdef\nSWARM_AUTH_TOKEN=x"))
        assertNull(cleanKeyInput("\"quoted-key-1234567\""))
        assertNull(cleanKeyInput("a".repeat(600)))
    }
}
