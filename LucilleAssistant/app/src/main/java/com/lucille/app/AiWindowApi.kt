package com.lucille.app

import org.json.JSONArray
import org.json.JSONObject

// ─── AI window ───────────────────────────────────────────────────────────────
// Which brains Lucille has: an OpenRouter key, a Claude key and an ElevenLabs voice key you can
// give her from the phone, where her replies come from, and her three offline models. The PC keeps the keys (mode 0600);
// this app sends a key once and never stores it. The PC never sends one back: only "set / where
// from / last four characters".

data class AiKey(
    val provider: String, // "openrouter" | "anthropic"
    val label: String,
    val set: Boolean,
    val source: String, // "window" | "env" | ""
    val last4: String,
    val envVar: String
)

data class AiLocalModel(val name: String, val model: String, val role: String, val installed: Boolean)

data class AiLocal(
    val enabled: Boolean,
    val endpoint: String,
    val mode: String, // "chain" | "race"
    val reachable: Boolean,
    val models: List<AiLocalModel>
)

data class AiState(
    val keys: List<AiKey>,
    /** "auto" | "cloud" | "local" */
    val source: String,
    /** "openrouter" | "anthropic" */
    val cloudFirst: String,
    val local: AiLocal,
    val claudeModel: String,
    val openRouterModel: String,
    val voiceId: String
)

data class VoiceOption(
    val id: String,
    val name: String,
    val category: String,
    val description: String,
    val accent: String,
    val gender: String,
    val age: String,
    val useCase: String,
    val previewUrl: String,
    val usable: Boolean
)

/** Pure JSON -> state, so it can be unit-tested without a phone or a PC. */
fun parseAiState(j: JSONObject): AiState {
    fun JSONArray?.objects(): List<JSONObject> = if (this == null) emptyList() else (0 until length()).mapNotNull { optJSONObject(it) }
    val keys = j.optJSONArray("keys").objects().map {
        AiKey(
            it.optString("provider"), it.optString("label"), it.optBoolean("set"),
            it.optString("source"), it.optString("last4"), it.optString("env_var")
        )
    }
    val prefs = j.optJSONObject("prefs")
    val l = j.optJSONObject("local")
    val local = AiLocal(
        enabled = l?.optBoolean("enabled") ?: false,
        endpoint = l?.optString("endpoint").orEmpty(),
        mode = l?.optString("mode").orEmpty().ifBlank { "chain" },
        reachable = l?.optBoolean("reachable") ?: false,
        models = l?.optJSONArray("models").objects().map {
            AiLocalModel(it.optString("name"), it.optString("model"), it.optString("role"), it.optBoolean("installed"))
        }
    )
    return AiState(
        keys = keys,
        source = prefs?.optString("source").orEmpty().ifBlank { "auto" },
        cloudFirst = prefs?.optString("cloud_first").orEmpty().ifBlank { "openrouter" },
        local = local,
        claudeModel = j.optJSONObject("claude")?.optString("model").orEmpty(),
        openRouterModel = j.optJSONObject("openrouter")?.optString("model").orEmpty(),
        voiceId = prefs?.optString("voice_id").orEmpty()
    )
}

/** "openrouter" (her voice through the same key already used for chat, no separate ElevenLabs
 * account) or "elevenlabs" (a direct ElevenLabs account's real catalog, with previews). */
data class VoiceList(val voices: List<VoiceOption>, val reason: String, val via: String)

/** Pure JSON -> voice list, so it can be unit-tested without a phone or a PC. */
fun parseVoiceOptions(j: JSONObject): VoiceList {
    val arr = j.optJSONArray("voices")
    val voices = if (arr == null) emptyList() else (0 until arr.length()).mapNotNull { arr.optJSONObject(it) }.map {
        VoiceOption(
            id = it.optString("voice_id"), name = it.optString("name"), category = it.optString("category"),
            description = it.optString("description"), accent = it.optString("accent"), gender = it.optString("gender"),
            age = it.optString("age"), useCase = it.optString("use_case"), previewUrl = it.optString("preview_url"),
            usable = it.optBoolean("usable", true)
        )
    }
    return VoiceList(voices, j.optString("reason"), j.optString("via"))
}

/** A key as typed or pasted: trimmed, and refused here (before it leaves the phone) if the PC would refuse it. */
fun cleanKeyInput(raw: String): String? {
    val k = raw.trim()
    if (k.length < 8 || k.length > 512) return null
    if (k.any { it <= ' ' || it.code >= 0x7f || it == '"' || it == '\'' || it == '\\' }) return null
    return k
}

private fun ApiReply?.aiOutcome(): Outcome<JSONObject> {
    if (this == null) return Outcome.Fail("Can't reach your PC. Check the address in Config and that Lucille Assistant is running.")
    val j = json
    if (code == 404 && j == null) return Outcome.Fail("Your PC is running an older Lucille Assistant. Update it to 1.7 or newer.")
    if (code in 200..299 && j != null && j.optBoolean("ok")) return Outcome.Ok(j)
    return Outcome.Fail(
        j?.optString("error").orEmpty().ifBlank {
            if (code == 401) "The PC refused this app's token. Pair this phone again in Config." else "The PC answered HTTP $code."
        }
    )
}

suspend fun loadAiState(serverBase: String, token: String): Outcome<AiState> =
    when (val r = lucilleRequest(serverBase, token, "GET", "/api/ai").aiOutcome()) {
        is Outcome.Ok -> Outcome.Ok(parseAiState(r.value))
        is Outcome.Fail -> r
    }

/** Saves a key on the PC. An empty [key] clears the one saved through this window. */
suspend fun saveAiKey(serverBase: String, token: String, provider: String, key: String): Outcome<Unit> =
    when (val r = lucilleRequest(
        serverBase, token, "POST", "/api/ai/key",
        JSONObject().put("provider", provider).put("key", key)
    ).aiOutcome()) {
        is Outcome.Ok -> Outcome.Ok(Unit)
        is Outcome.Fail -> r
    }

/** Asks the provider (for free) whether the saved key is valid: "valid" | "rejected" | "unreachable" | "no_key" | "unexpected". */
suspend fun testAiKey(serverBase: String, token: String, provider: String): Outcome<String> =
    when (val r = lucilleRequest(
        serverBase, token, "POST", "/api/ai/key/test", JSONObject().put("provider", provider)
    ).aiOutcome()) {
        is Outcome.Ok -> Outcome.Ok(r.value.optString("result"))
        is Outcome.Fail -> r
    }

suspend fun saveAiPrefs(serverBase: String, token: String, source: String? = null, cloudFirst: String? = null): Outcome<Unit> {
    val body = JSONObject()
    if (source != null) body.put("source", source)
    if (cloudFirst != null) body.put("cloud_first", cloudFirst)
    return when (val r = lucilleRequest(serverBase, token, "POST", "/api/ai/prefs", body).aiOutcome()) {
        is Outcome.Ok -> Outcome.Ok(Unit)
        is Outcome.Fail -> r
    }
}

/** Every voice on the ElevenLabs account tied to the saved key — her own name, labels and a
 * preview, never a list this app invented. Empty with a reason when there is no key yet, or
 * ElevenLabs could not be reached or rejected it. */
suspend fun loadVoiceOptions(serverBase: String, token: String): Outcome<VoiceList> =
    when (val r = lucilleRequest(serverBase, token, "GET", "/api/ai/voices").aiOutcome()) {
        is Outcome.Ok -> Outcome.Ok(parseVoiceOptions(r.value))
        is Outcome.Fail -> r
    }

/** Picks which voice she speaks with. An empty [voiceId] goes back to her usual default and fallback chain. */
suspend fun saveVoice(serverBase: String, token: String, voiceId: String): Outcome<Unit> =
    when (val r = lucilleRequest(
        serverBase, token, "POST", "/api/ai/voice", JSONObject().put("voice_id", voiceId)
    ).aiOutcome()) {
        is Outcome.Ok -> Outcome.Ok(Unit)
        is Outcome.Fail -> r
    }
