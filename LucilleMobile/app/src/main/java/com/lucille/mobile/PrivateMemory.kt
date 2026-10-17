package com.lucille.mobile

import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.DataInputStream
import java.io.DataOutputStream
import java.io.File
import java.security.SecureRandom
import java.util.Base64
import javax.crypto.Cipher
import javax.crypto.SecretKey
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.PBEKeySpec
import javax.crypto.spec.SecretKeySpec

/**
 * The private memory drive: things the user tells Lucille to keep to herself. It is
 * deliberately NOT swarm-memory (that syncs to GitHub and other devices): entries live
 * only in this app's private storage, each one encrypted on its own with an AES-256-GCM
 * key that never leaves the Android Keystore. Nothing here is read by any sync, and the
 * only way out is a passphrase-protected export the user starts themselves.
 *
 * Lose the key (new phone, cleared app data) and the entries are unreadable by design;
 * the passphrase export is the backup.
 */
interface MemoryCipher {
    fun encrypt(plain: ByteArray): ByteArray
    fun decrypt(blob: ByteArray): ByteArray
}

/** AES-256-GCM, random 12-byte IV prepended. A wrong key or a changed byte makes decrypt throw. */
class AesGcmCipher(private val key: SecretKey) : MemoryCipher {
    override fun encrypt(plain: ByteArray): ByteArray {
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.ENCRYPT_MODE, key)   // the Keystore picks the IV; software keys too
        return c.iv + c.doFinal(plain)
    }

    override fun decrypt(blob: ByteArray): ByteArray {
        require(blob.size > IV_BYTES + 16) { "Too short to be an encrypted entry." }
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, blob, 0, IV_BYTES))
        return c.doFinal(blob, IV_BYTES, blob.size - IV_BYTES)
    }

    companion object {
        const val IV_BYTES = 12

        /** A key from a passphrase, for exports only. */
        fun fromPassphrase(passphrase: CharArray, salt: ByteArray): AesGcmCipher {
            val f = SecretKeyFactory.getInstance("PBKDF2WithHmacSHA256")
            val raw = f.generateSecret(PBEKeySpec(passphrase, salt, 210_000, 256)).encoded
            return AesGcmCipher(SecretKeySpec(raw, "AES"))
        }
    }
}

class PrivateMemoryStore(private val file: File, private val cipher: MemoryCipher) {
    data class Entry(val id: String, val time: Long, val text: String)

    @Synchronized
    fun add(text: String, now: Long = System.currentTimeMillis()): Entry {
        val t = text.trim()
        require(t.isNotEmpty()) { "Nothing to remember." }
        require(t.length <= MAX_CHARS) { "That's too long for one entry (max $MAX_CHARS characters)." }
        val e = Entry(randomId(), now, t)
        file.parentFile?.mkdirs()
        file.appendText(Base64.getEncoder().encodeToString(cipher.encrypt(pack(e))) + "\n")
        return e
    }

    /** Newest first. Lines that fail to decrypt (wrong key, damage) are skipped, never guessed at. */
    @Synchronized
    fun list(): List<Entry> = readAll().sortedByDescending { it.time }

    fun search(q: String): List<Entry> {
        val needle = q.trim().lowercase()
        return if (needle.isEmpty()) list() else list().filter { it.text.lowercase().contains(needle) }
    }

    @Synchronized
    fun delete(id: String): Boolean {
        val keep = readAll().filter { it.id != id }
        val before = readAll().size
        if (keep.size == before) return false
        file.writeText(keep.joinToString("") { Base64.getEncoder().encodeToString(cipher.encrypt(pack(it))) + "\n" })
        return true
    }

    /** Passphrase-protected backup: "LPM1" + salt + encrypted bundle of every entry. */
    @Synchronized
    fun export(passphrase: CharArray): ByteArray {
        require(passphrase.size >= 8) { "Use a passphrase of at least 8 characters." }
        val salt = ByteArray(16).also { SecureRandom().nextBytes(it) }
        val bundle = ByteArrayOutputStream()
        DataOutputStream(bundle).use { o ->
            val all = readAll()
            o.writeInt(all.size)
            all.forEach { e -> o.writeUTF(e.id); o.writeLong(e.time); o.writeUTF(e.text) }
        }
        return MAGIC + salt + AesGcmCipher.fromPassphrase(passphrase, salt).encrypt(bundle.toByteArray())
    }

    /** Adds entries from an [export]; ones already here (same id) are skipped. Returns how many were new. */
    @Synchronized
    fun import(blob: ByteArray, passphrase: CharArray): Int {
        require(blob.size > MAGIC.size + 16 && blob.copyOfRange(0, MAGIC.size).contentEquals(MAGIC)) { "That isn't a Lucille private-memory backup." }
        val salt = blob.copyOfRange(MAGIC.size, MAGIC.size + 16)
        val plain = try {
            AesGcmCipher.fromPassphrase(passphrase, salt).decrypt(blob.copyOfRange(MAGIC.size + 16, blob.size))
        } catch (e: Exception) {
            throw IllegalArgumentException("Wrong passphrase, or the backup is damaged.")
        }
        val have = readAll().map { it.id }.toMutableSet()
        var added = 0
        DataInputStream(ByteArrayInputStream(plain)).use { i ->
            repeat(i.readInt()) {
                val e = Entry(i.readUTF(), i.readLong(), i.readUTF())
                if (have.add(e.id)) {
                    file.parentFile?.mkdirs()
                    file.appendText(Base64.getEncoder().encodeToString(cipher.encrypt(pack(e))) + "\n")
                    added++
                }
            }
        }
        return added
    }

    private fun readAll(): List<Entry> {
        if (!file.exists()) return emptyList()
        return file.readLines().mapNotNull { line ->
            if (line.isBlank()) return@mapNotNull null
            try { unpack(cipher.decrypt(Base64.getDecoder().decode(line.trim()))) } catch (_: Exception) { null }
        }
    }

    private fun pack(e: Entry): ByteArray {
        val b = ByteArrayOutputStream()
        DataOutputStream(b).use { it.writeUTF(e.id); it.writeLong(e.time); it.writeUTF(e.text) }
        return b.toByteArray()
    }

    private fun unpack(b: ByteArray): Entry =
        DataInputStream(ByteArrayInputStream(b)).use { Entry(it.readUTF(), it.readLong(), it.readUTF()) }

    private fun randomId(): String = ByteArray(8).also { SecureRandom().nextBytes(it) }.joinToString("") { "%02x".format(it) }

    companion object {
        const val MAX_CHARS = 8000   // writeUTF caps at 64 KB of encoded bytes; 8000 chars is safe in any script
        private val MAGIC = "LPM1".toByteArray()
    }
}
