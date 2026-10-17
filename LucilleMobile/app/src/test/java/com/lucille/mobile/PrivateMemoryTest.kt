package com.lucille.mobile

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import javax.crypto.KeyGenerator

class PrivateMemoryTest {
    @get:Rule val tmp = TemporaryFolder()

    private fun cipher() = AesGcmCipher(KeyGenerator.getInstance("AES").apply { init(256) }.generateKey())
    private fun store(c: MemoryCipher = cipher()) = PrivateMemoryStore(tmp.newFile().also { it.delete() }, c)

    @Test fun addListSearchDelete() {
        val s = store()
        val a = s.add("my sister's birthday is 3 March", now = 1)
        s.add("Dentist on Friday", now = 2)
        assertEquals(listOf("Dentist on Friday", "my sister's birthday is 3 March"), s.list().map { it.text })
        assertEquals(listOf(a.id), s.search("BIRTHDAY").map { it.id })
        assertTrue(s.delete(a.id))
        assertFalse(s.delete(a.id))
        assertEquals(1, s.list().size)
    }

    @Test fun nothingReadableOnDisk() {
        val f = tmp.newFile().also { it.delete() }
        PrivateMemoryStore(f, cipher()).add("the safe code is 4417")
        val raw = f.readText()
        assertFalse(raw.contains("4417"))
        assertFalse(raw.contains("safe"))
    }

    @Test fun anotherKeyReadsNothing() {
        val f = tmp.newFile().also { it.delete() }
        PrivateMemoryStore(f, cipher()).add("secret")
        assertTrue(PrivateMemoryStore(f, cipher()).list().isEmpty())
    }

    @Test fun tamperedLineIsSkippedNotMisread() {
        val f = tmp.newFile().also { it.delete() }
        val c = cipher()
        val s = PrivateMemoryStore(f, c)
        s.add("one"); s.add("two")
        val lines = f.readLines().toMutableList()
        lines[0] = lines[0].replaceRange(20, 21, if (lines[0][20] == 'A') "B" else "A")
        f.writeText(lines.joinToString("\n") + "\n")
        assertEquals(listOf("two"), PrivateMemoryStore(f, c).list().map { it.text })
    }

    @Test fun exportImportRoundTripAndWrongPassphrase() {
        val a = store()
        a.add("alpha", now = 1); a.add("beta", now = 2)
        val blob = a.export("correct horse".toCharArray())
        assertFalse(String(blob, Charsets.ISO_8859_1).contains("alpha"))

        val b = store()
        assertThrows(IllegalArgumentException::class.java) { b.import(blob, "wrong passphrase".toCharArray()) }
        assertEquals(2, b.import(blob, "correct horse".toCharArray()))
        assertEquals(0, b.import(blob, "correct horse".toCharArray()))   // again: all already here
        assertEquals(setOf("alpha", "beta"), b.list().map { it.text }.toSet())
    }

    @Test fun refusesEmptyShortPassphraseAndOversize() {
        val s = store()
        assertThrows(IllegalArgumentException::class.java) { s.add("   ") }
        assertThrows(IllegalArgumentException::class.java) { s.add("x".repeat(PrivateMemoryStore.MAX_CHARS + 1)) }
        assertThrows(IllegalArgumentException::class.java) { s.export("short".toCharArray()) }
    }
}
