package com.lucille.app

import android.content.Context
import android.media.MediaPlayer
import android.util.Log
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import kotlinx.coroutines.launch

// The AI window (Config -> "Lucille's brains"): give her an OpenRouter or Claude key, test it,
// choose where her replies come from, and see whether her three offline models are installed.
//
// Keys are typed into a hidden field, sent once, and the field is emptied. They are kept on the PC
// (a 0600 file), never on the phone, and the PC never sends one back. Nothing here uses
// rememberSaveable for a key, so a key never lands in the saved-state bundle either.

@Composable
fun AiWindowDialog(palette: DashboardPalette, serverHost: String, token: String, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    var state by remember { mutableStateOf<AiState?>(null) }
    var loadError by remember { mutableStateOf<String?>(null) }
    var message by remember { mutableStateOf<String?>(null) }

    suspend fun reload() {
        when (val r = loadAiState(serverHost, token)) {
            is Outcome.Ok -> { state = r.value; loadError = null }
            is Outcome.Fail -> loadError = r.message
        }
    }
    LaunchedEffect(serverHost, token) { reload() }

    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Card(
            colors = CardDefaults.cardColors(containerColor = palette.panel.copy(alpha = 1f)),
            shape = RoundedCornerShape(16.dp),
            border = BorderStroke(1.dp, palette.pink.copy(alpha = 0.5f)),
            modifier = Modifier
                .fillMaxWidth()
                .statusBarsPadding()
                .navigationBarsPadding()
                .imePadding()
                .padding(10.dp)
        ) {
            Column(
                modifier = Modifier.padding(14.dp).verticalScroll(rememberScrollState()),
                verticalArrangement = Arrangement.spacedBy(10.dp)
            ) {
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
                    Text("🧠 Lucille's brains", color = palette.pink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
                    TextButton(onClick = onDismiss) { Text("Close", color = palette.glow) }
                }

                val st = state
                if (st == null) {
                    Text(loadError ?: "Asking your PC…", color = if (loadError != null) palette.pink else palette.smoke, fontSize = 12.sp)
                    if (loadError != null) OutlinedButton(onClick = { scope.launch { reload() } }) { Text("Try again", color = palette.glow) }
                } else {
                    Text(
                        "Keys are kept on your PC, never on this phone, and are never sent back here.",
                        color = palette.smoke, fontSize = 10.sp
                    )
                    message?.let { Text(it, color = palette.glow, fontSize = 12.sp) }

                    st.keys.forEach { k ->
                        KeyRow(palette, k, serverHost, token) { msg ->
                            message = msg
                            scope.launch { reload() }
                        }
                        HorizontalDivider(color = palette.pink.copy(alpha = 0.2f))
                    }

                    Text("Where she answers from", color = palette.text, fontSize = 13.sp, fontWeight = FontWeight.Bold)
                    // Chat providers only: ElevenLabs is a voice key, not a chat source.
                    val chatKeysSet = st.keys.count { it.set && (it.provider == "openrouter" || it.provider == "anthropic") }
                    val anyCloud = chatKeysSet > 0
                    Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                        listOf("auto" to "Auto", "cloud" to "Cloud only", "local" to "Offline only").forEach { (id, label) ->
                            ChoiceButton(palette, st.source == id, label) {
                                scope.launch {
                                    message = when (val r = saveAiPrefs(serverHost, token, source = id)) {
                                        is Outcome.Ok -> null
                                        is Outcome.Fail -> r.message
                                    }
                                    reload()
                                }
                            }
                        }
                    }
                    Text(
                        when (st.source) {
                            "cloud" -> "Cloud only: if the cloud is down she falls back to a canned line, never the offline models."
                            "local" -> "Offline only: nothing leaves this PC and no key is used."
                            else -> if (anyCloud) "Auto: cloud first, then her offline models, then a canned line." else "Auto: no cloud key yet, so she uses her offline models."
                        },
                        color = palette.smoke, fontSize = 10.sp
                    )
                    if (chatKeysSet > 1 && st.source != "local") {
                        Text("Ask first", color = palette.text, fontSize = 12.sp)
                        Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                            listOf("openrouter" to "OpenRouter", "anthropic" to "Claude").forEach { (id, label) ->
                                ChoiceButton(palette, st.cloudFirst == id, label) {
                                    scope.launch {
                                        message = when (val r = saveAiPrefs(serverHost, token, cloudFirst = id)) {
                                            is Outcome.Ok -> null
                                            is Outcome.Fail -> r.message
                                        }
                                        reload()
                                    }
                                }
                            }
                        }
                    }
                    Text("Claude model: ${st.claudeModel}   ·   OpenRouter model: ${st.openRouterModel}", color = palette.smoke, fontSize = 10.sp)

                    HorizontalDivider(color = palette.pink.copy(alpha = 0.2f))
                    OfflineSwarm(palette, st.local)

                    if (st.keys.any { (it.provider == "eleven" || it.provider == "openrouter") && it.set }) {
                        HorizontalDivider(color = palette.pink.copy(alpha = 0.2f))
                        VoicePicker(palette, serverHost, token, st.voiceId) { msg ->
                            message = msg
                            scope.launch { reload() }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun KeyRow(palette: DashboardPalette, k: AiKey, serverHost: String, token: String, onDone: (String) -> Unit) {
    val scope = rememberCoroutineScope()
    // Deliberately remember, not rememberSaveable: a key must not be written to the saved-state bundle.
    var input by remember { mutableStateOf("") }
    var show by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var local by remember { mutableStateOf<String?>(null) }

    val colors = OutlinedTextFieldDefaults.colors(
        focusedBorderColor = palette.pink, unfocusedBorderColor = palette.smoke,
        focusedTextColor = palette.text, unfocusedTextColor = palette.text,
        focusedLabelColor = palette.glow, unfocusedLabelColor = palette.smoke, cursorColor = palette.pink
    )

    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        Text(k.label, color = palette.text, fontSize = 13.sp, fontWeight = FontWeight.Bold)
        Text(
            when {
                !k.set -> "No key yet."
                k.source == "window" -> "A key is saved on your PC${if (k.last4.isNotBlank()) " (ends …${k.last4})" else ""}."
                else -> "A key is set in her .env on your PC (${k.envVar})${if (k.last4.isNotBlank()) ", ends …${k.last4}" else ""}."
            },
            color = if (k.set) palette.glow else palette.smoke, fontSize = 11.sp
        )
        OutlinedTextField(
            value = input,
            onValueChange = { input = it },
            label = { Text(if (k.set) "Replace key" else "Paste key") },
            singleLine = true,
            visualTransformation = if (show) VisualTransformation.None else PasswordVisualTransformation(),
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
            colors = colors,
            shape = RoundedCornerShape(10.dp),
            textStyle = androidx.compose.ui.text.TextStyle(fontFamily = FontFamily.Monospace, fontSize = 12.sp),
            modifier = Modifier.fillMaxWidth()
        )
        Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
            Button(
                enabled = !busy && input.isNotBlank(),
                onClick = {
                    val clean = cleanKeyInput(input)
                    if (clean == null) {
                        local = "That doesn't look like a key: no spaces, quotes or line breaks, 8 to 512 characters."
                        return@Button
                    }
                    scope.launch {
                        busy = true; local = null
                        when (val r = saveAiKey(serverHost, token, k.provider, clean)) {
                            is Outcome.Ok -> { input = ""; show = false; onDone("${k.label} key saved. Tap Test to check it.") }
                            is Outcome.Fail -> local = r.message
                        }
                        busy = false
                    }
                },
                colors = ButtonDefaults.buttonColors(containerColor = palette.pink)
            ) { Text("Save") }
            OutlinedButton(
                enabled = !busy && k.set,
                onClick = {
                    scope.launch {
                        busy = true; local = null
                        when (val r = testAiKey(serverHost, token, k.provider)) {
                            is Outcome.Ok -> onDone(
                                when (r.value) {
                                    "valid" -> "${k.label}: the key works."
                                    "rejected" -> "${k.label} says this key is not valid."
                                    "unreachable" -> "${k.label} could not be reached from your PC."
                                    "no_key" -> "${k.label}: no key to test."
                                    else -> "${k.label} gave an unexpected answer."
                                }
                            )
                            is Outcome.Fail -> local = r.message
                        }
                        busy = false
                    }
                }
            ) { Text("Test", color = palette.glow) }
            if (k.source == "window") {
                OutlinedButton(
                    enabled = !busy,
                    onClick = {
                        scope.launch {
                            busy = true; local = null
                            when (val r = saveAiKey(serverHost, token, k.provider, "")) {
                                is Outcome.Ok -> onDone("${k.label} key removed from your PC.")
                                is Outcome.Fail -> local = r.message
                            }
                            busy = false
                        }
                    }
                ) { Text("Remove", color = palette.glow) }
            }
            Spacer(Modifier.weight(1f))
            TextButton(onClick = { show = !show }) { Text(if (show) "Hide" else "Show", color = palette.smoke, fontSize = 11.sp) }
        }
        local?.let { Text(it, color = palette.pink, fontSize = 11.sp) }
    }
}

@Composable
private fun OfflineSwarm(palette: DashboardPalette, l: AiLocal) {
    Text("Offline swarm (three models on your PC)", color = palette.text, fontSize = 13.sp, fontWeight = FontWeight.Bold)
    Text(
        when {
            !l.enabled -> "Switched off in config.json."
            l.reachable -> "Model server answering at ${l.endpoint}. Mode: ${if (l.mode == "race") "race (all three at once)" else "chain (one after another)"}."
            else -> "No model server at ${l.endpoint}. Start Ollama (or llama-server) on your PC; nothing is downloaded for you."
        },
        color = if (l.enabled && l.reachable) palette.glow else palette.smoke, fontSize = 11.sp
    )
    l.models.forEach { m ->
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Text("${m.name}  ·  ${m.model}", color = palette.text, fontSize = 12.sp, fontFamily = FontFamily.Monospace)
                if (m.role.isNotBlank()) Text(m.role, color = palette.smoke, fontSize = 10.sp)
            }
            Text(
                if (m.installed) "ready" else if (l.reachable) "not pulled" else "—",
                color = if (m.installed) palette.glow else palette.smoke, fontSize = 11.sp
            )
        }
        if (l.reachable && !m.installed) {
            Text("On your PC:  ollama pull ${m.model}", color = palette.smoke, fontSize = 10.sp, fontFamily = FontFamily.Monospace)
        }
    }
    Text(
        "Models, mode and address are set in ~/.config/lucille-assistant/config.json (\"local_models\"), not from the phone.",
        color = palette.smoke, fontSize = 10.sp
    )
}

/** Plays an ElevenLabs preview straight from its URL — nothing saved to disk, no base64 round trip. */
private fun playVoicePreview(context: Context, url: String) {
    try {
        MediaPlayer().apply {
            setAudioAttributes(
                android.media.AudioAttributes.Builder()
                    .setContentType(android.media.AudioAttributes.CONTENT_TYPE_SPEECH)
                    .setUsage(android.media.AudioAttributes.USAGE_MEDIA)
                    .build()
            )
            setDataSource(url)
            setOnPreparedListener { it.start() }
            setOnCompletionListener { it.release() }
            setOnErrorListener { mp, _, _ -> mp.release(); true }
            prepareAsync()
        }
    } catch (e: Exception) {
        Log.e("LucilleAudio", "Failed to play voice preview", e)
    }
}

/**
 * Every voice on the ElevenLabs account tied to the saved key — her own name, labels and a
 * preview, never a list this app invented. [currentVoiceId] is what she uses now ("" = her
 * usual default and fallback chain).
 */
@Composable
private fun VoicePicker(palette: DashboardPalette, serverHost: String, token: String, currentVoiceId: String, onDone: (String) -> Unit) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var voices by remember { mutableStateOf<List<VoiceOption>?>(null) }
    var reason by remember { mutableStateOf("") }
    var via by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }

    LaunchedEffect(serverHost, token) {
        when (val r = loadVoiceOptions(serverHost, token)) {
            is Outcome.Ok -> { voices = r.value.voices; reason = r.value.reason; via = r.value.via }
            is Outcome.Fail -> { voices = emptyList(); reason = "" }
        }
    }

    Text("Her voice", color = palette.text, fontSize = 13.sp, fontWeight = FontWeight.Bold)
    val vs = voices
    when {
        vs == null -> Text("Asking…", color = palette.smoke, fontSize = 12.sp)
        reason == "rejected" -> Text("ElevenLabs rejected that key — Test it above.", color = palette.smoke, fontSize = 12.sp)
        reason == "unreachable" -> Text("Could not reach ElevenLabs from your PC.", color = palette.smoke, fontSize = 12.sp)
        vs.isEmpty() -> Text("No voices available for this account.", color = palette.smoke, fontSize = 12.sp)
        else -> {
            Text(
                (if (via == "openrouter") "The voices OpenRouter accepts for her — no separate ElevenLabs account needed, billed through your OpenRouter key."
                 else "Every voice on your ElevenLabs account — not a list this app made up.") +
                    (if (currentVoiceId.isEmpty()) " Using her usual default voice." else ""),
                color = palette.smoke, fontSize = 10.sp
            )
            Column(
                modifier = Modifier.heightIn(max = 280.dp).verticalScroll(rememberScrollState()),
                verticalArrangement = Arrangement.spacedBy(10.dp)
            ) {
                vs.forEach { v ->
                    Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
                        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
                            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                                if (v.previewUrl.isNotBlank()) {
                                    IconButton(onClick = { playVoicePreview(context, v.previewUrl) }, modifier = Modifier.size(28.dp)) {
                                        Text("▶", color = palette.glow, fontSize = 13.sp)
                                    }
                                }
                                Text(v.name, color = palette.text, fontSize = 13.sp, fontWeight = FontWeight.Bold)
                            }
                            if (v.id == currentVoiceId) {
                                Text("in use", color = palette.glow, fontSize = 11.sp)
                            } else {
                                OutlinedButton(
                                    enabled = !busy,
                                    onClick = {
                                        scope.launch {
                                            busy = true
                                            onDone(
                                                when (val r = saveVoice(serverHost, token, v.id)) {
                                                    is Outcome.Ok -> "${v.name}: now her voice."
                                                    is Outcome.Fail -> r.message
                                                }
                                            )
                                            busy = false
                                        }
                                    },
                                    contentPadding = PaddingValues(horizontal = 10.dp, vertical = 4.dp)
                                ) { Text("Use this voice", color = palette.glow, fontSize = 11.sp) }
                            }
                        }
                        val labels = listOf(v.category, v.accent, v.gender, v.age, v.useCase).filter { it.isNotBlank() }
                        if (labels.isNotEmpty() || v.description.isNotBlank()) {
                            Text(
                                labels.joinToString(" · ") + (if (v.description.isNotBlank()) " — ${v.description}" else "") +
                                    (if (!v.usable) " (this account cannot use it)" else ""),
                                color = palette.smoke, fontSize = 10.sp
                            )
                        }
                    }
                }
            }
            if (currentVoiceId.isNotEmpty()) {
                OutlinedButton(
                    enabled = !busy,
                    onClick = {
                        scope.launch {
                            busy = true
                            onDone(
                                when (val r = saveVoice(serverHost, token, "")) {
                                    is Outcome.Ok -> "Back to her default voice."
                                    is Outcome.Fail -> r.message
                                }
                            )
                            busy = false
                        }
                    }
                ) { Text("Use the default voice instead", color = palette.glow, fontSize = 12.sp) }
            }
        }
    }
}

@Composable
private fun ChoiceButton(palette: DashboardPalette, selected: Boolean, label: String, onClick: () -> Unit) {
    if (selected) {
        Button(onClick = onClick, colors = ButtonDefaults.buttonColors(containerColor = palette.pink), contentPadding = PaddingValues(horizontal = 12.dp, vertical = 6.dp)) {
            Text(label, fontSize = 12.sp)
        }
    } else {
        OutlinedButton(onClick = onClick, contentPadding = PaddingValues(horizontal = 12.dp, vertical = 6.dp)) {
            Text(label, color = palette.glow, fontSize = 12.sp)
        }
    }
}
