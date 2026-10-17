package com.lucille.mobile

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import java.io.File
import java.security.KeyStore
import java.util.Base64
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey

/** The sudo-code secret, encrypted at rest with a Keystore key that never leaves the phone's secure hardware. */
object SudoCodeStore {
    private const val ALIAS = "lucille_sudo_code_v1"

    private fun file(ctx: Context) = File(ctx.filesDir, "sudo-code/secret.enc")

    fun exists(ctx: Context) = file(ctx).isFile

    /** Returns null when saved, otherwise what to fix. */
    fun save(ctx: Context, base32: String): String? {
        val secret = Totp.decodeBase32(base32) ?: return "That isn't a valid secret. Type it exactly as the PC printed it."
        file(ctx).apply { parentFile?.mkdirs(); writeText(Base64.getEncoder().encodeToString(AesGcmCipher(key()).encrypt(secret))) }
        return null
    }

    fun load(ctx: Context): ByteArray? = try {
        AesGcmCipher(key()).decrypt(Base64.getDecoder().decode(file(ctx).readText().trim()))
    } catch (_: Exception) { null }

    fun clear(ctx: Context) { file(ctx).delete() }

    private fun key(): SecretKey {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val spec = KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).setKeySize(256).build()
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").run { init(spec); generateKey() }
    }
}
