package com.lucille.app

import java.nio.charset.StandardCharsets
import java.security.MessageDigest
import java.util.Base64

/** Wire model and canonical signed bytes shared with internal/unlockproto in Go. */
data class SudoUnlockRequest(
    val id: String,
    val nonce: String,
    val choices: List<String>,
    val service: String,
    val user: String,
    val host: String,
    val expires: Long
)

object SudoUnlockProtocol {
    const val VERSION = "lucille-unlock-v1"

    fun message(request: SudoUnlockRequest, chosen: String): ByteArray {
        require(request.id.matches(Regex("^[0-9a-f]{32}$"))) { "Invalid request id" }
        require(request.nonce.matches(Regex("^[0-9a-f]{32}$"))) { "Invalid request nonce" }
        require(request.choices.size == 3 && request.choices.distinct().size == 3 &&
            request.choices.all { it.matches(Regex("^[0-9]{4}$")) }) { "Invalid number choices" }
        require(chosen in request.choices) { "Choose one of the numbers shown by the PC" }
        listOf(request.service, request.user, request.host).forEach {
            require(it.isNotBlank() && it.length <= 64 && !it.any { ch -> ch == '\n' || ch == '\r' || ch == '\u0000' }) {
                "Invalid request context"
            }
        }
        require(request.expires > 0) { "Invalid request expiry" }
        return listOf(
            VERSION,
            "id=${request.id}",
            "nonce=${request.nonce}",
            "chosen=$chosen",
            "service=${request.service}",
            "user=${request.user}",
            "host=${request.host}",
            "expires=${request.expires}"
        ).joinToString("\n").toByteArray(StandardCharsets.UTF_8)
    }

    fun sha256(message: ByteArray): ByteArray = MessageDigest.getInstance("SHA-256").digest(message)

    fun signatureBase64(signature: ByteArray): String = Base64.getEncoder().encodeToString(signature)
}
