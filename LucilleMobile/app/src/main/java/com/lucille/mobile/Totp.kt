package com.lucille.mobile

import java.nio.ByteBuffer
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec

/**
 * The rotating 6-digit code for "sudo with the phone plugged in" (RFC 6238, HMAC-SHA1, 30 s: the same code an
 * authenticator app shows). The PC's lucille-usbpam computes it from the same secret and compares. Nothing is sent
 * anywhere: the code is computed on the phone and typed by the user.
 */
object Totp {
    const val STEP = 30
    const val DIGITS = 6

    fun code(secret: ByteArray, unixSeconds: Long): String {
        val mac = Mac.getInstance("HmacSHA1").apply { init(SecretKeySpec(secret, "HmacSHA1")) }
        val sum = mac.doFinal(ByteBuffer.allocate(8).putLong(unixSeconds / STEP).array())
        val off = sum[sum.size - 1].toInt() and 0x0f
        val n = ((sum[off].toInt() and 0x7f) shl 24) or ((sum[off + 1].toInt() and 0xff) shl 16) or
            ((sum[off + 2].toInt() and 0xff) shl 8) or (sum[off + 3].toInt() and 0xff)
        return (n % 1_000_000).toString().padStart(DIGITS, '0')
    }

    fun secondsLeft(unixSeconds: Long): Int = STEP - (unixSeconds % STEP).toInt()

    /** Base32 the way the PC prints it: any case, spaces/dashes/padding ignored. Null if it isn't valid or is too short. */
    fun decodeBase32(text: String): ByteArray? {
        val alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
        val clean = text.uppercase().filterNot { it == ' ' || it == '-' || it == '=' || it == '\n' || it == '\t' }
        if (clean.isEmpty()) return null
        var bits = 0; var acc = 0
        val out = ArrayList<Byte>()
        for (ch in clean) {
            val v = alphabet.indexOf(ch)
            if (v < 0) return null
            acc = (acc shl 5) or v; bits += 5
            if (bits >= 8) { bits -= 8; out.add(((acc shr bits) and 0xff).toByte()) }
        }
        return if (out.size < 10) null else out.toByteArray()
    }
}
