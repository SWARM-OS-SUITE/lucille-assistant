package com.lucille.app

import android.util.Base64
import org.json.JSONArray
import org.json.JSONObject
import java.util.Locale

object SudoApprovalApi {
    suspend fun pending(server: String, token: String): Outcome<List<SudoUnlockRequest>> {
        val reply = lucilleRequest(server, token, "GET", "/api/unlock/pending")
            ?: return Outcome.Fail("Can't reach Lucille Assistant on the PC.")
        if (reply.code !in 200..299) return Outcome.Fail(apiError(reply))
        val entries = reply.json?.optJSONArray("requests")
            ?: return Outcome.Fail("The PC returned an invalid approval list.")
        val requests = mutableListOf<SudoUnlockRequest>()
        for (index in 0 until entries.length()) {
            val json = entries.optJSONObject(index) ?: continue
            try {
                val request = SudoUnlockRequest(
                    id = json.getString("id"),
                    nonce = json.getString("nonce"),
                    choices = json.getJSONArray("choices").toStringList(),
                    service = json.getString("service"),
                    user = json.getString("user"),
                    host = json.getString("host"),
                    expires = json.getLong("expires")
                )
                require(request.service == "sudo") { "Only sudo approvals are supported here" }
                val now = System.currentTimeMillis() / 1000
                require(request.expires > now && request.expires <= now + 120) { "Expired or overlong sudo request" }
                request.choices.forEach { SudoUnlockProtocol.message(request, it) }
                requests += request
            } catch (_: Exception) {
                // Never offer a malformed, unsupported, or non-sudo request for approval.
            }
        }
        return Outcome.Ok(requests)
    }

    suspend fun enroll(server: String, token: String, code: String, publicKeyDer: ByteArray): Outcome<String> {
        if (!code.matches(Regex("^[0-9]{6}$"))) return Outcome.Fail("Enter the six-digit code shown on the PC.")
        val body = JSONObject().apply {
            put("code", code)
            put("pubkey", Base64.encodeToString(publicKeyDer, Base64.NO_WRAP))
        }
        val reply = lucilleRequest(server, token, "POST", "/api/unlock/enrol", body)
            ?: return Outcome.Fail("Can't reach Lucille Assistant on the PC.")
        if (reply.code !in 200..299) return Outcome.Fail(apiError(reply))
        return Outcome.Ok(reply.json?.optString("next").orEmpty())
    }

    suspend fun respond(server: String, token: String, id: String, chosen: String, signature: ByteArray): Outcome<Unit> {
        val body = JSONObject().apply {
            put("id", id)
            put("chosen", chosen)
            put("sig", SudoUnlockProtocol.signatureBase64(signature))
        }
        val reply = lucilleRequest(server, token, "POST", "/api/unlock/respond", body)
            ?: return Outcome.Fail("Can't reach Lucille Assistant on the PC.")
        return if (reply.code in 200..299 && reply.json?.optBoolean("ok") == true) {
            Outcome.Ok(Unit)
        } else {
            Outcome.Fail(apiError(reply))
        }
    }

    private fun JSONArray.toStringList(): List<String> = List(length()) { getString(it) }

    private fun apiError(reply: ApiReply): String {
        val message = reply.json?.optString("error").orEmpty()
        return when {
            message.isNotBlank() -> message
            reply.code == 401 || reply.code == 403 -> "The PC refused this phone. Pair it with the PC first."
            reply.code == 404 -> "This PC version does not support phone sudo approvals yet."
            else -> String.format(Locale.ROOT, "The PC answered HTTP %d.", reply.code)
        }
    }
}
