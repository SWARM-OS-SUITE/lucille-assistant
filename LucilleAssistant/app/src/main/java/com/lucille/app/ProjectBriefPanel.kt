package com.lucille.app

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.widget.Toast
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.launch
import java.time.LocalDate
import java.time.format.DateTimeFormatter

// ─── The brief, laid out like an artifact ────────────────────────────────────
// A title bar, a document body, and code blocks with a Copy button. "Ready to run"
// means a command you copy and paste on the PC: this app never runs one for you.

private val Good = Color(0xFF58D68D)
private val Warn = Color(0xFFFFC857)
private val Bad = Color(0xFFFF4D6A)
private val Mono = FontFamily.Monospace

/** The Briefs dock tab: today's brief first, then the editable project documents and the Team Brief (threads, coder jobs). */
@Composable
fun BriefsHub(palette: DashboardPalette, serverHost: String, token: String) {
    var tab by rememberSaveable { mutableStateOf("brief") }
    Column(modifier = Modifier.fillMaxSize(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            HubTab("🧾 Brief", tab == "brief", palette) { tab = "brief" }
            HubTab("📋 Docs", tab == "docs", palette) { tab = "docs" }
            HubTab("🤝 Team", tab == "team", palette) { tab = "team" }
        }
        Box(modifier = Modifier.weight(1f)) {
            when (tab) {
                "docs" -> BriefingsPanel(palette, serverHost, token)
                "team" -> TeamBriefPanel(palette, serverHost, token)
                else -> ProjectBriefPanel(palette, serverHost, token)
            }
        }
    }
}

@Composable
private fun HubTab(label: String, selected: Boolean, palette: DashboardPalette, onClick: () -> Unit) {
    Surface(
        onClick = onClick,
        color = if (selected) palette.pink.copy(alpha = 0.25f) else palette.panel,
        shape = RoundedCornerShape(12.dp),
        border = BorderStroke(1.dp, palette.pink.copy(alpha = if (selected) 0.8f else 0.3f))
    ) {
        Text(label, color = palette.text, fontSize = 12.sp, fontWeight = if (selected) FontWeight.Bold else FontWeight.Normal,
            modifier = Modifier.padding(horizontal = 14.dp, vertical = 7.dp))
    }
}

private data class BoltzDraft(val project: BriefProject, val s: BriefSuggestion)

/** "Approve and start" (user 2026-10-15): the formal prompt Lucille drafted, waiting for the person's OK. */
private data class StartDraft(val project: BriefProject, val s: BriefSuggestion, val prompt: String)

@Composable
fun ProjectBriefPanel(palette: DashboardPalette, serverHost: String, token: String) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val say: (String) -> Unit = { Toast.makeText(context, it, Toast.LENGTH_SHORT).show() }

    var brief by remember { mutableStateOf<ProjectBrief?>(null) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var openId by rememberSaveable { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(setOf<String>()) }
    var draft by remember { mutableStateOf<BoltzDraft?>(null) }
    var startDraft by remember { mutableStateOf<StartDraft?>(null) }
    var activeThread by remember { mutableStateOf<Pair<String, String>?>(null) } // anchor to quote

    suspend fun load() {
        loading = true
        when (val r = loadProjectBrief(context, serverHost, token)) {
            is Outcome.Ok -> {
                brief = r.value; error = null
                if (openId == null) openId = r.value.projects.firstOrNull()?.id
            }
            is Outcome.Fail -> {
                error = r.message
                // Whatever is still on screen is no longer live: say so, and turn the PC-only buttons off.
                brief = brief?.copy(mode = "cached", asOf = "Showing the last brief this screen loaded; it could not be refreshed")
            }
        }
        loading = false
    }
    fun working(key: String, on: Boolean) { busy = if (on) busy + key else busy - key }

    LaunchedEffect(serverHost, token) { load() }

    val online = brief?.mode == "pc"
    val frame = palette.pink.copy(alpha = 0.4f)

    Card(
        colors = CardDefaults.cardColors(containerColor = palette.panel),
        shape = RoundedCornerShape(16.dp),
        border = BorderStroke(1.dp, frame),
        modifier = Modifier.fillMaxSize()
    ) {
        Column(modifier = Modifier.fillMaxSize()) {
            // Title bar, like the chrome around an artifact.
            Row(
                modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                Text("🧾", fontSize = 14.sp)
                Text("lucille-brief.md", color = palette.text, fontFamily = Mono, fontSize = 12.sp, fontWeight = FontWeight.Bold, modifier = Modifier.weight(1f))
                brief?.let { Chip(modeLabel(it.mode), modeColor(it.mode)) }
                if (loading) CircularProgressIndicator(modifier = Modifier.size(16.dp), strokeWidth = 2.dp, color = palette.pink)
                else Text("↻", color = palette.pink, fontSize = 18.sp, modifier = Modifier.clickable { scope.launch { load() } }.padding(horizontal = 6.dp))
            }
            HorizontalDivider(color = frame)

            Column(
                modifier = Modifier
                    .fillMaxSize()
                    .background(Color.Black.copy(alpha = 0.28f))
                    .verticalScroll(rememberScrollState())
                    .padding(14.dp),
                verticalArrangement = Arrangement.spacedBy(10.dp)
            ) {
                Text("Project brief", color = palette.text, fontSize = 20.sp, fontWeight = FontWeight.Bold)
                Text(LocalDate.now().format(DateTimeFormatter.ofPattern("EEEE d MMMM yyyy")), color = palette.smoke, fontSize = 11.sp)

                error?.let { Callout(it, Bad, palette) { scope.launch { load() } } }

                brief?.let { b ->
                    val tone = if (b.mode == "pc") Good else Warn
                    val read = if (b.filesTotal > 0) " · read ${b.filesRead} of ${b.filesTotal} files" else ""
                    Callout(b.asOf.trimEnd('.') + read + ".", tone, palette, null)

                    b.projects.forEach { p ->
                        ProjectSection(
                            p = p, palette = palette, open = openId == p.id, online = online, mdWritable = b.mdWritable,
                            busy = busy,
                            onToggle = { openId = if (openId == p.id) null else p.id },
                            onCopy = { copyText(context, it); say("Copied. Paste it in a terminal on the PC.") },
                            onThink = {
                                scope.launch {
                                    working("think:${p.id}", true)
                                    when (val r = thinkSuggestions(serverHost, token, p.id)) {
                                        is Outcome.Ok -> { say("Lucille thought up ${r.value} new ones."); load() }
                                        is Outcome.Fail -> say(r.message)
                                    }
                                    working("think:${p.id}", false)
                                }
                            },
                            onAddMd = { s ->
                                scope.launch {
                                    working("md:${s.id}", true)
                                    when (val r = addSuggestionToMd(serverHost, token, p.id, s)) {
                                        is Outcome.Ok -> { say(if (r.value) "Added to the project file." else "It was already in the project file."); load() }
                                        is Outcome.Fail -> say(r.message)
                                    }
                                    working("md:${s.id}", false)
                                }
                            },
                            onBoltz = { s -> draft = BoltzDraft(p, s) },
                            onStart = { s ->
                                scope.launch {
                                    working("start:${s.id}", true)
                                    when (val r = draftJob(serverHost, token, s.id, "claude")) {
                                        is Outcome.Ok -> startDraft = StartDraft(p, s, r.value)
                                        is Outcome.Fail -> say(r.message)
                                    }
                                    working("start:${s.id}", false)
                                }
                            },
                            onComment = { anchor, quote -> activeThread = anchor to quote },
                            onDeal = { s ->
                                scope.launch {
                                    working("deal:${s.id}", true)
                                    when (val r = dealThread(serverHost, token, s.id, "", "${s.title}: ${s.why}", "auto")) {
                                        is Outcome.Ok -> {
                                            say(if (r.value.launched) "Told ${r.value.target} to deal with it now." else "Queued for ${r.value.target}.")
                                            load()
                                        }
                                        is Outcome.Fail -> say(r.message)
                                    }
                                    working("deal:${s.id}", false)
                                }
                            }
                        )
                    }
                }
                if (brief == null && loading) {
                    Text("Reading your project files…", color = palette.smoke, fontSize = 12.sp)
                }
                Spacer(Modifier.height(8.dp))
            }
        }
    }

    activeThread?.let { (anchor, quote) ->
        ThreadDialog(
            anchor = anchor,
            quote = quote,
            state = null,
            palette = palette,
            serverHost = serverHost,
            token = token,
            onChanged = { scope.launch { load() } },
            onClose = { activeThread = null }
        )
    }

    startDraft?.let { d ->
        StartDialog(
            draft = d, palette = palette, starting = "go:${d.s.id}" in busy,
            onCancel = { startDraft = null },
            onStart = { prompt ->
                scope.launch {
                    working("go:${d.s.id}", true)
                    when (val r = createJob(serverHost, token, d.s.id, "claude", prompt, launch = true)) {
                        is Outcome.Ok -> { say("Approved. Lucille starts Claude on the PC (if two Claude sessions are already open it waits in the mailbox). Follow it in the Team Brief under this suggestion."); startDraft = null }
                        is Outcome.Fail -> say(r.message)
                    }
                    working("go:${d.s.id}", false)
                }
            }
        )
    }

    draft?.let { d ->
        BoltzDialog(
            draft = d, palette = palette, sending = "send:${d.s.id}" in busy,
            onCancel = { draft = null },
            onSend = { title, body ->
                scope.launch {
                    working("send:${d.s.id}", true)
                    when (val r = sendSuggestionToBoltz(serverHost, token, d.project.id, d.s.id, title, body, d.s.size)) {
                        is Outcome.Ok -> { say("Queued for Dr. Boltz as ${r.value}. Approve the result in the Bridge."); draft = null; load() }
                        is Outcome.Fail -> say(r.message)
                    }
                    working("send:${d.s.id}", false)
                }
            }
        )
    }
}

private fun modeLabel(mode: String) = when (mode) {
    "pc" -> "PC live"
    "git" -> "from git"
    else -> "cached"
}

private fun modeColor(mode: String) = if (mode == "pc") Good else Warn

private fun copyText(context: Context, text: String) {
    val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
    cm.setPrimaryClip(ClipData.newPlainText("Lucille", text))
}

@Composable
private fun ProjectSection(
    p: BriefProject, palette: DashboardPalette, open: Boolean, online: Boolean, mdWritable: Boolean, busy: Set<String>,
    onToggle: () -> Unit, onCopy: (String) -> Unit, onThink: () -> Unit, onAddMd: (BriefSuggestion) -> Unit, onBoltz: (BriefSuggestion) -> Unit,
    onStart: (BriefSuggestion) -> Unit, onComment: (String, String) -> Unit, onDeal: (BriefSuggestion) -> Unit
) {
    Card(
        colors = CardDefaults.cardColors(containerColor = palette.panel.copy(alpha = 0.55f)),
        shape = RoundedCornerShape(12.dp),
        border = BorderStroke(1.dp, palette.pink.copy(alpha = if (open) 0.5f else 0.2f))
    ) {
        Column(modifier = Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Row(
                modifier = Modifier.fillMaxWidth().clickable(onClick = onToggle),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                Chip(p.code.ifBlank { "··" }, palette.pink)
                Column(modifier = Modifier.weight(1f)) {
                    Text(p.title, color = palette.text, fontSize = 14.sp, fontWeight = FontWeight.Bold)
                    Text(headline(p), color = palette.smoke, fontSize = 10.sp)
                }
                Text(if (open) "▾" else "▸", color = palette.pink, fontSize = 16.sp)
            }
            if (!open) return@Column

            if (p.summary.isNotBlank()) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(modifier = Modifier.weight(1f)) {
                        Text(p.summary, color = palette.text, fontSize = 11.sp)
                        if (p.stale) Text("The briefing text above was written a while ago; the build list below is read fresh.", color = Warn, fontSize = 10.sp)
                    }
                    BriefButton("💬 Discuss", palette) { onComment("proj:${p.id}", "${p.title}: ${p.summary}") }
                }
            }

            Heading("OPEN ON THE BUILD LIST", palette)
            if (p.tracker.source == "none") {
                Text("I could not read the build list for this project, so I won't guess.", color = Warn, fontSize = 11.sp)
            } else {
                p.tracker.open.forEach { r ->
                    Row(
                        modifier = Modifier.fillMaxWidth().clickable { onComment("item:${r.id}", "${r.id}: ${r.item}") }.padding(vertical = 3.dp),
                        horizontalArrangement = Arrangement.spacedBy(6.dp),
                        verticalAlignment = Alignment.CenterVertically
                    ) {
                        Text(r.id, color = palette.pink, fontFamily = Mono, fontSize = 10.sp)
                        Text(r.p, color = priorityColor(r.p, palette), fontFamily = Mono, fontSize = 10.sp)
                        Text(r.item, color = palette.text, fontSize = 11.sp, modifier = Modifier.weight(1f))
                        Text(r.status.take(14), color = palette.smoke, fontFamily = Mono, fontSize = 9.sp)
                        Text("💬", fontSize = 11.sp, color = palette.glow)
                    }
                }
                if (p.tracker.open.isEmpty()) Text("Nothing open. ${p.tracker.done} done.", color = Good, fontSize = 11.sp)
                Text(
                    "${p.tracker.open.size} shown · ${p.tracker.total} tracked · ${p.tracker.done} done" +
                        if (p.tracker.source == "git") " · from git" else "",
                    color = palette.smoke, fontSize = 9.sp
                )
            }

            Heading("FILES I READ", palette)
            p.files.forEach { f ->
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.Top) {
                    Text(fileMark(f), fontSize = 11.sp, color = fileColor(f))
                    Column {
                        Text(f.name, color = palette.text, fontSize = 11.sp, fontFamily = Mono)
                        if (f.reason.isNotBlank()) Text(f.reason, color = palette.smoke, fontSize = 9.sp)
                    }
                }
            }

            Heading(
                "SUGGESTIONS" + when (p.suggestionsSource) {
                    "model" -> " · Lucille's"
                    "briefing" -> " · from the briefing"
                    "tracker" -> " · from the build list"
                    else -> ""
                }, palette
            )
            if (p.suggestions.isEmpty()) {
                Text("Nothing to suggest yet.", color = palette.smoke, fontSize = 11.sp)
            }
            p.suggestions.forEach { s ->
                SuggestionCard(p, s, palette, online, mdWritable, busy, onCopy, onAddMd, onBoltz, onStart, onComment, onDeal)
            }
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                BriefButton(
                    if ("think:${p.id}" in busy) "Thinking…" else "✨ Ask Lucille for fresh ones", palette,
                    enabled = online && "think:${p.id}" !in busy, onClick = onThink
                )
            }
            if (!online) Text("Needs your PC (and a working language model).", color = palette.smoke, fontSize = 9.sp)

            if (p.command.isNotBlank()) {
                Heading("CATCH ME UP", palette)
                CodeBlock("bash", p.command, palette) { onCopy(p.command) }
            }
        }
    }
}

private fun headline(p: BriefProject): String {
    val parts = mutableListOf<String>()
    if (p.tracker.source != "none") parts += (if (p.tracker.open.size >= 6) "6+" else "${p.tracker.open.size}") + " open"
    val read = p.files.count { it.source == "pc" || it.source == "git" }
    parts += "$read/${p.files.size} files"
    p.gitDirty?.takeIf { it > 0 }?.let { parts += "$it uncommitted" }
    if (p.suggestions.isNotEmpty()) parts += "${p.suggestions.size} suggestions"
    return parts.joinToString(" · ")
}

@Composable
private fun SuggestionCard(
    p: BriefProject, s: BriefSuggestion, palette: DashboardPalette, online: Boolean, mdWritable: Boolean, busy: Set<String>,
    onCopy: (String) -> Unit, onAddMd: (BriefSuggestion) -> Unit, onBoltz: (BriefSuggestion) -> Unit,
    onStart: (BriefSuggestion) -> Unit, onComment: (String, String) -> Unit, onDeal: (BriefSuggestion) -> Unit
) {
    Card(
        colors = CardDefaults.cardColors(containerColor = Color.Black.copy(alpha = 0.25f)),
        shape = RoundedCornerShape(10.dp),
        border = BorderStroke(1.dp, palette.pink.copy(alpha = 0.18f))
    ) {
        Column(modifier = Modifier.padding(10.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(s.title, color = palette.text, fontSize = 12.sp, fontWeight = FontWeight.Bold, modifier = Modifier.weight(1f))
                Chip(s.size, palette.smoke)
            }
            if (s.why.isNotBlank()) Text(s.why, color = palette.smoke, fontSize = 11.sp)
            CodeBlock("bash", s.command, palette) { onCopy(s.command) }

            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.horizontalScroll(rememberScrollState())) {
                BriefButton(
                    if ("deal:${s.id}" in busy) "Dealing…" else "⚡ Deal with it", palette, filled = true,
                    enabled = online && "deal:${s.id}" !in busy
                ) { onDeal(s) }
                BriefButton("💬 Comment", palette) { onComment(s.id, "${s.title}: ${s.why}") }
                BriefButton(
                    if ("start:${s.id}" in busy) "Drafting…" else "▶ Approve and start", palette,
                    enabled = online && "start:${s.id}" !in busy
                ) { onStart(s) }
                when {
                    s.inMd -> BriefButton("✓ In project md", palette, enabled = false) {}
                    else -> BriefButton(
                        if ("md:${s.id}" in busy) "Adding…" else "＋ Project md", palette,
                        enabled = online && mdWritable && "md:${s.id}" !in busy
                    ) { onAddMd(s) }
                }
                when {
                    s.sent.isNotBlank() -> BriefButton("✓ Boltz ${s.sent}" + if (s.sentStatus.isNotBlank()) " · ${s.sentStatus}" else "", palette, enabled = false) {}
                    else -> BriefButton("→ Send to Boltz", palette, enabled = online && p.canSend) { onBoltz(s) }
                }
            }
            val why = when {
                !online -> "Adding and sending need your PC."
                !s.inMd && !mdWritable -> "The project file isn't writable on the PC right now."
                s.sent.isBlank() && !p.canSend -> p.sendNote
                else -> ""
            }
            if (why.isNotBlank()) Text(why, color = palette.smoke, fontSize = 9.sp)
        }
    }
}

@Composable
private fun StartDialog(draft: StartDraft, palette: DashboardPalette, starting: Boolean, onCancel: () -> Unit, onStart: (String) -> Unit) {
    var prompt by remember(draft.prompt) { mutableStateOf(draft.prompt) }
    AlertDialog(
        onDismissRequest = { if (!starting) onCancel() },
        containerColor = Color(0xFF1A0410),
        titleContentColor = palette.pink,
        textContentColor = palette.text,
        title = { Text("Start Claude on the PC?", fontSize = 16.sp) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.verticalScroll(rememberScrollState())) {
                // The limits are the launcher's own (launcher.go); this only says them out loud.
                Text(
                    "Lucille starts Claude in the ${draft.project.title} folder with this prompt. It may edit files and run builds and tests there. " +
                        "It can't commit, push, delete or use sudo. One job at a time, 30 minutes at most, and you can stop it from the Team Brief. " +
                        "The suggestion came from a file or a model, so read it first.",
                    color = palette.smoke, fontSize = 11.sp
                )
                OutlinedTextField(
                    prompt, { prompt = it }, label = { Text("Prompt") }, minLines = 6, maxLines = 14,
                    textStyle = androidx.compose.ui.text.TextStyle(fontSize = 11.sp, fontFamily = Mono, color = palette.text),
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedBorderColor = palette.pink, unfocusedBorderColor = palette.pink.copy(alpha = 0.4f), cursorColor = palette.pink
                    ),
                    modifier = Modifier.fillMaxWidth()
                )
            }
        },
        confirmButton = {
            Button(
                onClick = { onStart(prompt.trim()) },
                enabled = !starting && prompt.isNotBlank(),
                colors = ButtonDefaults.buttonColors(containerColor = palette.pink)
            ) { Text(if (starting) "Starting…" else "Approve and start") }
        },
        dismissButton = { TextButton(onClick = onCancel, enabled = !starting) { Text("Cancel", color = palette.smoke) } }
    )
}

@Composable
private fun BoltzDialog(draft: BoltzDraft, palette: DashboardPalette, sending: Boolean, onCancel: () -> Unit, onSend: (String, String) -> Unit) {
    var title by remember { mutableStateOf(draft.s.title) }
    var body by remember { mutableStateOf(draft.s.body.ifBlank { draft.s.why } + "\n\nDone means: ") }
    val fieldColors = OutlinedTextFieldDefaults.colors(
        focusedTextColor = palette.text, unfocusedTextColor = palette.text,
        focusedBorderColor = palette.pink, unfocusedBorderColor = palette.pink.copy(alpha = 0.4f),
        cursorColor = palette.pink
    )
    AlertDialog(
        onDismissRequest = { if (!sending) onCancel() },
        containerColor = Color(0xFF1A0410),
        titleContentColor = palette.pink,
        textContentColor = palette.text,
        title = { Text("Send to Dr. Boltz", fontSize = 16.sp) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.verticalScroll(rememberScrollState())) {
                Text(
                    "Goes to the ${draft.project.repo} repo. He codes it on a branch and nothing lands until you approve it in the Bridge. " +
                        "This text came from a file or a model, so read it and say what \"done\" means.",
                    color = palette.smoke, fontSize = 11.sp
                )
                OutlinedTextField(title, { title = it }, label = { Text("Job") }, singleLine = true, colors = fieldColors, modifier = Modifier.fillMaxWidth())
                OutlinedTextField(body, { body = it }, label = { Text("What he is told") }, minLines = 5, maxLines = 10, colors = fieldColors, modifier = Modifier.fillMaxWidth())
            }
        },
        confirmButton = {
            Button(
                onClick = { onSend(title.trim(), body.trim()) },
                enabled = !sending && title.isNotBlank() && body.isNotBlank(),
                colors = ButtonDefaults.buttonColors(containerColor = palette.pink)
            ) { Text(if (sending) "Sending…" else "Send") }
        },
        dismissButton = { TextButton(onClick = onCancel, enabled = !sending) { Text("Cancel", color = palette.smoke) } }
    )
}

// ─── Small pieces ────────────────────────────────────────────────────────────

@Composable
private fun CodeBlock(lang: String, text: String, palette: DashboardPalette, onCopy: () -> Unit) {
    Surface(
        color = Color(0xFF0B0309),
        shape = RoundedCornerShape(10.dp),
        border = BorderStroke(1.dp, palette.pink.copy(alpha = 0.25f))
    ) {
        Column {
            Row(
                modifier = Modifier.fillMaxWidth().background(Color.White.copy(alpha = 0.06f)).padding(start = 10.dp),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Text(lang, color = palette.smoke, fontFamily = Mono, fontSize = 10.sp)
                Text("Copy", color = palette.pink, fontSize = 11.sp, fontWeight = FontWeight.Bold,
                    modifier = Modifier.clickable(onClick = onCopy).padding(horizontal = 12.dp, vertical = 8.dp))
            }
            SelectionContainer {
                Text(text, color = palette.text, fontFamily = Mono, fontSize = 11.sp, softWrap = false,
                    modifier = Modifier.horizontalScroll(rememberScrollState()).padding(10.dp))
            }
        }
    }
}

@Composable
private fun Heading(text: String, palette: DashboardPalette) {
    Text(text, color = palette.pink, fontSize = 10.sp, fontWeight = FontWeight.Bold, letterSpacing = 1.sp, modifier = Modifier.padding(top = 4.dp))
}

@Composable
private fun Chip(text: String, color: Color) {
    Surface(color = color.copy(alpha = 0.16f), shape = RoundedCornerShape(8.dp), border = BorderStroke(1.dp, color.copy(alpha = 0.5f))) {
        Text(text, color = color, fontSize = 9.sp, fontWeight = FontWeight.Bold, modifier = Modifier.padding(horizontal = 7.dp, vertical = 2.dp))
    }
}

@Composable
private fun Callout(text: String, tone: Color, palette: DashboardPalette, onRetry: (() -> Unit)?) {
    Surface(color = tone.copy(alpha = 0.10f), shape = RoundedCornerShape(10.dp), border = BorderStroke(1.dp, tone.copy(alpha = 0.55f))) {
        Row(modifier = Modifier.padding(10.dp), horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
            Text(text, color = palette.text, fontSize = 11.sp, modifier = Modifier.weight(1f))
            if (onRetry != null) BriefButton("Retry", palette, onClick = onRetry)
        }
    }
}

@Composable
private fun BriefButton(label: String, palette: DashboardPalette, filled: Boolean = false, enabled: Boolean = true, onClick: () -> Unit) {
    if (filled) {
        Button(
            onClick = onClick, enabled = enabled,
            colors = ButtonDefaults.buttonColors(containerColor = palette.pink),
            contentPadding = PaddingValues(horizontal = 12.dp, vertical = 0.dp),
            modifier = Modifier.height(34.dp)
        ) { Text(label, fontSize = 11.sp, fontWeight = FontWeight.Bold, maxLines = 1) }
    } else {
        OutlinedButton(
            onClick = onClick, enabled = enabled,
            border = BorderStroke(1.dp, palette.pink.copy(alpha = if (enabled) 0.7f else 0.12f)),
            contentPadding = PaddingValues(horizontal = 12.dp, vertical = 0.dp),
            modifier = Modifier.height(34.dp)
        ) { Text(label, color = if (enabled) palette.text else palette.smoke.copy(alpha = 0.4f), fontSize = 11.sp, maxLines = 1) }
    }
}

private fun fileMark(f: BriefFileInfo) = when (f.source) {
    "pc" -> "✓"
    "git" -> "☁"
    "blocked" -> "⛔"
    else -> "✗"
}

private fun fileColor(f: BriefFileInfo) = when (f.source) {
    "pc", "git" -> Good
    "blocked" -> Bad
    else -> Warn
}

private fun priorityColor(p: String, palette: DashboardPalette) = when (p) {
    "P0" -> Bad
    "P1" -> Warn
    else -> palette.smoke
}
