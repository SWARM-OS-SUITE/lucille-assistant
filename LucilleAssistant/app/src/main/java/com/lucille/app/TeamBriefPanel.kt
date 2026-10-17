package com.lucille.app

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.graphics.Bitmap
import android.widget.Toast
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.time.OffsetDateTime

// ─── Team Brief ───────────────────────────────────────────────────────────────────────────────
// What needs you, what is critical, who is on what, each project, and whether everything started: one scrolling
// page. Tap ANY statement to open its thread: write in it, ask Lucille or Dr. Boltz to answer, or have Lucille draft
// a prompt for Claude / Antigravity / ChatGPT. The page refreshes itself every few seconds, so a reply typed on the
// PC (or the SWARM OS app) shows up here without a pull-to-refresh.

private val TBad = Color(0xFFFF4433)
private val TWarn = Color(0xFFFFC04D)
private val TGood = Color(0xFF4ADE80)
private val CoderGreen = Color(0xFF39D98A)
private val BoltzRed = Color(0xFFFF6A5A)
private val Ink = Color(0xFF1A0410)

private fun tLevelColor(level: String, palette: DashboardPalette) = when (level) {
    "bad" -> TBad
    "warn" -> TWarn
    "ok" -> TGood
    else -> palette.pink
}

internal fun tAgoText(iso: String): String = try {
    val m = java.time.Duration.between(OffsetDateTime.parse(iso), OffsetDateTime.now()).toMinutes()
    when {
        m < 1 -> "now"
        m < 60 -> "${m}m ago"
        m < 1440 -> "${m / 60}h ago"
        else -> "${m / 1440}d ago"
    }
} catch (_: Exception) {
    ""
}

internal fun copyToClipboard(context: Context, label: String, text: String) {
    (context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager).setPrimaryClip(ClipData.newPlainText(label, text))
}

@Composable
internal fun TButton(text: String, palette: DashboardPalette, onClick: () -> Unit) {
    Button(
        onClick = onClick, shape = RoundedCornerShape(50), contentPadding = PaddingValues(horizontal = 12.dp, vertical = 6.dp),
        colors = ButtonDefaults.buttonColors(containerColor = palette.pink.copy(alpha = 0.25f), contentColor = palette.text),
        border = BorderStroke(1.dp, palette.pink.copy(alpha = 0.5f))
    ) { Text(text, fontSize = 11.sp, fontWeight = FontWeight.SemiBold) }
}

/** A picture from Steam's CDN, loaded once and cached; shows a dark box while it loads or if it cannot. */
@Composable
fun RemoteImage(url: String, modifier: Modifier = Modifier, contentScale: ContentScale = ContentScale.Crop) {
    val bmp by produceState<Bitmap?>(initialValue = null, url) { value = if (url.isBlank()) null else loadRemoteBitmap(url) }
    val b = bmp
    if (b != null) Image(bitmap = b.asImageBitmap(), contentDescription = null, modifier = modifier, contentScale = contentScale)
    else Box(modifier.background(Color(0xFF111111)))
}

@Composable
fun TeamBriefPanel(palette: DashboardPalette, serverHost: String, token: String) {
    val context = LocalContext.current
    var team by remember { mutableStateOf<TeamState?>(null) }
    var error by remember { mutableStateOf<String?>(null) }
    var open by rememberSaveable { mutableStateOf<String?>(null) }
    var kick by remember { mutableIntStateOf(0) }

    LaunchedEffect(serverHost, token, kick) {
        while (true) {
            when (val r = fetchTeam(serverHost, token)) {
                is Outcome.Ok -> { team = r.value; error = null }
                is Outcome.Fail -> error = r.message
            }
            delay(if (team?.pending?.isNotEmpty() == true) 1500L else 4000L) // faster while an AI is writing
        }
    }

    Column(
        modifier = Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 10.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp)
    ) {
        val t = team
        Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
            Text("TEAM BRIEF", color = palette.glow, fontSize = 13.sp, fontWeight = FontWeight.Bold, letterSpacing = 2.sp)
            Spacer(Modifier.weight(1f))
            Text(if (error == null && t != null) "● live" else "○ reconnecting", color = if (error == null && t != null) TGood else palette.smoke, fontSize = 10.sp, fontFamily = FontFamily.Monospace)
        }
        if (error != null && t == null) {
            Text(error!!, color = TBad, fontSize = 12.sp)
            TButton("Retry", palette) { kick++ }
        }
        if (t == null && error == null) Text("Reading the projects…", color = palette.smoke, fontSize = 12.sp)
        t?.let { st ->
            Pills(st, palette)
            st.sections.forEach { sec ->
                Card(
                    colors = CardDefaults.cardColors(containerColor = palette.panel),
                    shape = RoundedCornerShape(14.dp),
                    border = BorderStroke(1.dp, palette.pink.copy(alpha = 0.25f))
                ) {
                    Column(Modifier.padding(horizontal = 12.dp, vertical = 8.dp)) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            Text(sec.title.uppercase(), color = palette.glow, fontSize = 10.sp, fontWeight = FontWeight.Bold, fontFamily = FontFamily.Monospace, letterSpacing = 1.5.sp)
                            Spacer(Modifier.weight(1f))
                            if (sec.note.isNotBlank()) Text(sec.note, color = palette.smoke, fontSize = 9.sp)
                        }
                        sec.items.forEach { s -> StatementRow(s, st, palette) { open = s.anchor } }
                    }
                }
            }
            Spacer(Modifier.height(8.dp))
        }
    }

    val anchor = open
    val st = team
    if (anchor != null && st != null) {
        val stmt = st.sections.flatMap { it.items }.firstOrNull { it.anchor == anchor }
        ThreadDialog(
            anchor = anchor,
            quote = stmt?.text ?: st.threads[anchor]?.firstOrNull { it.text.isNotBlank() }?.text.orEmpty(),
            state = st, palette = palette, serverHost = serverHost, token = token,
            onChanged = { kick++ }, onClose = { open = null }
        )
    }
}

@Composable
private fun Pills(st: TeamState, palette: DashboardPalette) {
    fun count(id: String) = st.sections.firstOrNull { it.id == id }?.items?.size ?: 0
    val health = st.sections.firstOrNull { it.id == "health" }?.items?.firstOrNull()
    Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        if (count("needs") > 0) TChip("${count("needs")} waiting on you", TBad)
        if (count("critical") > 0) TChip("${count("critical")} critical", TWarn)
        if (count("team") > 0) TChip("${count("team")} AI claims", palette.pink)
        if (health != null) TChip(health.tag.ifBlank { "health" }, tLevelColor(health.level, palette))
    }
}

@Composable
internal fun TChip(text: String, color: Color) {
    Text(
        text.uppercase(), color = color, fontSize = 9.sp, fontWeight = FontWeight.Bold, fontFamily = FontFamily.Monospace,
        modifier = Modifier.background(color.copy(alpha = 0.10f), RoundedCornerShape(50)).padding(horizontal = 8.dp, vertical = 4.dp)
    )
}

@Composable
private fun StatementRow(s: TStatement, st: TeamState, palette: DashboardPalette, onOpen: () -> Unit) {
    val n = st.threads[s.anchor]?.size ?: 0
    val thinking = st.pending.any { it.startsWith(s.anchor + "|") }
    Row(
        Modifier.fillMaxWidth().height(IntrinsicSize.Min).clickable { onOpen() }.padding(vertical = 8.dp),
        horizontalArrangement = Arrangement.spacedBy(10.dp)
    ) {
        Box(Modifier.width(3.dp).fillMaxHeight().background(tLevelColor(s.level, palette), RoundedCornerShape(2.dp)))
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(3.dp)) {
            Text(s.text, color = palette.text, fontSize = 13.sp)
            if (s.sub.isNotBlank()) Text(s.sub, color = palette.smoke, fontSize = 11.sp)
            Row(horizontalArrangement = Arrangement.spacedBy(5.dp)) {
                if (s.tag.isNotBlank()) TChip(s.tag, palette.smoke)
                if (s.status.isNotBlank() && s.status != "open") TChip(s.status, if (s.status == "done") TGood else palette.glow)
            }
        }
        Text(
            (if (thinking) "💬…" else "💬") + if (n > 0) " $n" else "",
            color = if (n > 0 || thinking) palette.glow else palette.smoke, fontSize = 12.sp, modifier = Modifier.align(Alignment.Top)
        )
    }
}

@Composable
internal fun ThreadDialog(
    anchor: String, quote: String, state: TeamState? = null, palette: DashboardPalette,
    serverHost: String, token: String, onChanged: () -> Unit, onClose: () -> Unit
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val say: (String) -> Unit = { Toast.makeText(context, it, Toast.LENGTH_SHORT).show() }
    var text by remember(anchor) { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var draft by remember(anchor) { mutableStateOf<Pair<String, String>?>(null) } // who, prompt
    var localState by remember { mutableStateOf(state) }

    LaunchedEffect(serverHost, token, state) {
        if (state != null) {
            localState = state
        } else {
            while (true) {
                when (val r = fetchTeam(serverHost, token)) {
                    is Outcome.Ok -> localState = r.value
                    is Outcome.Fail -> {}
                }
                delay(if (localState?.pending?.isNotEmpty() == true) 1500L else 4000L)
            }
        }
    }

    val st = localState ?: TeamState(emptyList(), emptyMap(), emptySet(), emptyMap(), 0L)
    val msgs = st.threads[anchor].orEmpty()
    val jobs = st.jobs[anchor].orEmpty()
    val scroll = rememberScrollState()
    LaunchedEffect(msgs.size, st.pending.size) { scroll.animateScrollTo(scroll.maxValue) }

    fun run(block: suspend () -> Unit) {
        if (busy) return
        busy = true
        scope.launch { try { block() } finally { busy = false } }
    }

    suspend fun sendIfAny(): Boolean {
        val t = text.trim()
        if (t.isEmpty()) return true
        return when (val r = postThread(serverHost, token, anchor, t, quote)) {
            is Outcome.Ok -> { text = ""; if (r.value.isNotBlank() && r.value != "noted") say("Lucille: ${r.value}"); true }
            is Outcome.Fail -> { say(r.message); false }
        }
    }

    Dialog(onDismissRequest = onClose, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Card(
            modifier = Modifier.fillMaxWidth().fillMaxHeight(0.94f).padding(6.dp).imePadding(),
            colors = CardDefaults.cardColors(containerColor = Ink),
            shape = RoundedCornerShape(16.dp),
            border = BorderStroke(1.dp, palette.pink.copy(alpha = 0.6f))
        ) {
            Column(Modifier.fillMaxSize().padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("COMMENT ON THIS", color = palette.glow, fontSize = 10.sp, fontWeight = FontWeight.Bold, fontFamily = FontFamily.Monospace, letterSpacing = 1.5.sp)
                    Spacer(Modifier.weight(1f))
                    TButton("Close", palette) { onClose() }
                }
                if (quote.isNotBlank()) Text(quote, color = palette.smoke, fontSize = 12.sp, maxLines = 4)

                Column(Modifier.weight(1f).verticalScroll(scroll), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    if (msgs.isEmpty()) Text("Nothing here yet. Say something, or ask one of them.", color = palette.smoke, fontSize = 12.sp)
                    msgs.forEach { Bubble(it, palette) }
                    st.pending.filter { it.startsWith("$anchor|") }.forEach {
                        val whoName = when {
                            it.endsWith("boltz") -> "Dr. Boltz"
                            it.endsWith("claude") -> "Claude"
                            it.endsWith("auto") -> "Claude / Next model"
                            else -> "Lucille"
                        }
                        Text("$whoName is thinking…", color = palette.smoke, fontSize = 12.sp)
                    }
                    if (jobs.isNotEmpty()) {
                        jobs.forEach { j ->
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                                val label = when (j.status) {
                                    "claimed" -> "→ ${j.to}: picked up by ${j.claimedBy}"
                                    "running" -> "→ ${j.to}: running on the PC (started by Lucille)"
                                    "failed" -> "→ ${j.to}: stopped, see the thread"
                                    "cancelled" -> "→ ${j.to}: taken back"
                                    "done" -> "→ ${j.to}: done"
                                    "manual" -> "→ ${j.to}: paste into ChatGPT"
                                    else -> "→ ${j.to}: waiting for a ${j.to} session"
                                }
                                TChip(label, CoderGreen)
                                if (j.status == "manual") TButton("Copy", palette) { copyToClipboard(context, "Prompt", j.prompt); say("Copied. Paste it into ChatGPT.") }
                                if (j.status == "queued" || j.status == "running") TButton(if (j.status == "running") "Stop it" else "Take back", palette) { run { cancelJob(serverHost, token, j.id); onChanged() } }
                            }
                        }
                    }
                    draft?.let { (who, prompt) ->
                        var edited by remember(prompt) { mutableStateOf(prompt) }
                        Card(colors = CardDefaults.cardColors(containerColor = Color(0x22112211)), border = BorderStroke(1.dp, CoderGreen.copy(alpha = 0.6f))) {
                            Column(Modifier.padding(10.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                                Text("Lucille's draft prompt for $who. Edit it, then approve.", color = palette.smoke, fontSize = 11.sp)
                                OutlinedTextField(value = edited, onValueChange = { edited = it }, modifier = Modifier.fillMaxWidth().heightIn(min = 140.dp, max = 260.dp), textStyle = androidx.compose.ui.text.TextStyle(fontSize = 11.sp, fontFamily = FontFamily.Monospace, color = palette.text))
                                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                    TButton(if (who == "chatgpt") "Approve and copy" else "Approve and send", palette) {
                                        run {
                                            when (val r = createJob(serverHost, token, anchor, who, edited)) {
                                                is Outcome.Ok -> {
                                                    if (who == "chatgpt") { copyToClipboard(context, "Prompt", r.value.prompt); say("Copied. Paste it into ChatGPT.") } else say("Queued for $who.")
                                                    draft = null; onChanged()
                                                }
                                                is Outcome.Fail -> say(r.message)
                                            }
                                        }
                                    }
                                    TButton("Cancel", palette) { draft = null }
                                }
                            }
                        }
                    }
                }

                OutlinedTextField(
                    value = text, onValueChange = { text = it }, modifier = Modifier.fillMaxWidth(), maxLines = 3,
                    placeholder = { Text("Say something or answer on this statement…", color = palette.smoke, fontSize = 12.sp) },
                    textStyle = androidx.compose.ui.text.TextStyle(fontSize = 13.sp, color = palette.text)
                )
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp), modifier = Modifier.horizontalScroll(rememberScrollState())) {
                    TButton("Send", palette) { run { if (sendIfAny()) onChanged() } }
                    TButton("⚡ Deal with it", palette) {
                        run {
                            val t = text.trim()
                            when (val r = dealThread(serverHost, token, anchor, t, quote, "auto")) {
                                is Outcome.Ok -> {
                                    text = ""
                                    say(if (r.value.launched) "Told ${r.value.target} to deal with it now." else "Queued for ${r.value.target}.")
                                    onChanged()
                                }
                                is Outcome.Fail -> say(r.message)
                            }
                        }
                    }
                    TButton("Ask Lucille", palette) { run { if (sendIfAny()) { askThread(serverHost, token, anchor, "lucille").let { if (it is Outcome.Fail) say(it.message) }; onChanged() } } }
                    TButton("Ask Claude", palette) { run { if (sendIfAny()) { askThread(serverHost, token, anchor, "claude").let { if (it is Outcome.Fail) say(it.message) }; onChanged() } } }
                    TButton("Ask Boltz", palette) { run { if (sendIfAny()) { askThread(serverHost, token, anchor, "boltz").let { if (it is Outcome.Fail) say(it.message) }; onChanged() } } }
                }
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    listOf("claude" to "Claude", "antigravity" to "Antigravity", "chatgpt" to "ChatGPT").forEach { (id, label) ->
                        TButton("Prompt $label", palette) {
                            run {
                                if (sendIfAny()) when (val r = draftJob(serverHost, token, anchor, id)) {
                                    is Outcome.Ok -> draft = id to r.value
                                    is Outcome.Fail -> say(r.message)
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun Bubble(m: TMsg, palette: DashboardPalette) {
    val (label, color) = when {
        m.by == "lucille" -> "Lucille" to palette.pink
        m.by == "boltz" -> "Dr. Boltz" to BoltzRed
        m.kind == "coder" -> m.by to CoderGreen
        m.by == "you" -> "You" to palette.glow
        else -> m.by to palette.glow
    }
    val mine = color == palette.glow
    Column(Modifier.fillMaxWidth(), horizontalAlignment = if (mine) Alignment.End else Alignment.Start) {
        Column(
            Modifier.fillMaxWidth(0.92f).background(color.copy(alpha = 0.12f), RoundedCornerShape(12.dp)).padding(10.dp),
            verticalArrangement = Arrangement.spacedBy(4.dp)
        ) {
            Text(
                label.uppercase() + "  " + tAgoText(m.at) + if (m.model.isNotBlank()) "  ·  " + m.model.substringAfterLast('/') else "",
                color = color, fontSize = 9.sp, fontWeight = FontWeight.Bold, fontFamily = FontFamily.Monospace
            )
            Text(m.text, color = if (m.err) palette.smoke else palette.text, fontSize = 13.sp)
        }
    }
}
