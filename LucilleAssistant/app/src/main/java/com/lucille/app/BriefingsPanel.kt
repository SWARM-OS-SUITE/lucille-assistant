package com.lucille.app

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.widget.Toast
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
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
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

// ─── Project Briefings ───────────────────────────────────────────────────────
// Read-first context for each project, kept on the PC by Lucille Assistant (1.1+).
// Everything here is a view of the PC's copy: nothing is stored on the phone.

/** Which briefing the editor is working on; existing == null means a new one. */
private data class EditorState(val existing: Briefing?)

@Composable
fun BriefingsPanel(palette: DashboardPalette, serverHost: String, token: String) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    // A toast, not a line in the header: the header is scrolled out of sight when you are at a briefing's buttons.
    val say: (String) -> Unit = { Toast.makeText(context, it, Toast.LENGTH_SHORT).show() }

    var briefings by remember { mutableStateOf<List<Briefing>?>(null) }
    var loading by remember { mutableStateOf(true) }
    var error by remember { mutableStateOf<String?>(null) }
    var openId by rememberSaveable { mutableStateOf<String?>(null) }
    var armedDeleteId by remember { mutableStateOf<String?>(null) }
    var editor by remember { mutableStateOf<EditorState?>(null) }
    var saving by remember { mutableStateOf(false) }
    var editorError by remember { mutableStateOf<String?>(null) }
    var activeThread by remember { mutableStateOf<Pair<String, String>?>(null) }

    suspend fun load() {
        loading = true
        when (val r = fetchBriefings(serverHost, token)) {
            is Outcome.Ok -> { briefings = r.value; error = null }
            is Outcome.Fail -> error = r.message
        }
        loading = false
    }

    LaunchedEffect(serverHost, token) { load() }
    // A delete has to be confirmed within a few seconds, otherwise the button resets.
    LaunchedEffect(armedDeleteId) {
        if (armedDeleteId != null) { delay(4000); armedDeleteId = null }
    }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState()),
        verticalArrangement = Arrangement.spacedBy(8.dp)
    ) {
        Card(
            colors = CardDefaults.cardColors(containerColor = palette.panel),
            shape = RoundedCornerShape(16.dp),
            border = BorderStroke(1.dp, palette.pink.copy(alpha = 0.4f))
        ) {
            Column(modifier = Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("📋 Project Briefings", color = palette.pink, fontSize = 14.sp, fontWeight = FontWeight.Bold)
                Text(
                    "Where each project stands, what comes next and when, plus 3 suggestions. Copy for an AI hands the same text to Claude and others. Lucille knows the one-line summaries too.",
                    color = palette.smoke, fontSize = 10.sp
                )
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                    SmallButton("＋ New", palette, filled = true) { editorError = null; editor = EditorState(null) }
                    SmallButton("↻ Refresh", palette) { scope.launch { load() } }
                    if (loading) CircularProgressIndicator(modifier = Modifier.size(16.dp), strokeWidth = 2.dp, color = palette.pink)
                }
            }
        }

        error?.let { message ->
            Card(
                colors = CardDefaults.cardColors(containerColor = palette.panel),
                shape = RoundedCornerShape(14.dp),
                border = BorderStroke(1.dp, palette.glow.copy(alpha = 0.5f))
            ) {
                Column(modifier = Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text(message, color = palette.text, fontSize = 11.sp)
                    SmallButton("Retry", palette) { scope.launch { load() } }
                }
            }
        }

        briefings?.let { list ->
            if (list.isEmpty() && error == null) {
                Text("No briefings yet. Tap ＋ New to add one.", color = palette.smoke, fontSize = 11.sp, modifier = Modifier.padding(6.dp))
            }
            list.forEach { b ->
                val open = openId == b.id
                Card(
                    colors = CardDefaults.cardColors(containerColor = palette.panel),
                    shape = RoundedCornerShape(14.dp),
                    border = BorderStroke(1.dp, palette.pink.copy(alpha = if (open) 0.55f else 0.25f))
                ) {
                    Column(modifier = Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .clickable { openId = if (open) null else b.id; armedDeleteId = null },
                            horizontalArrangement = Arrangement.spacedBy(8.dp),
                            verticalAlignment = Alignment.Top
                        ) {
                            if (b.project.isNotBlank()) {
                                Text(b.project, color = palette.glow, fontSize = 9.sp, fontWeight = FontWeight.Bold, fontFamily = FontFamily.Monospace)
                            }
                            Column(modifier = Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                                Text(b.title, color = palette.glow, fontSize = 13.sp, fontWeight = FontWeight.Bold)
                                Text(b.summary.ifBlank { "No summary yet." }, color = palette.text, fontSize = 11.sp)
                            }
                            Text(if (open) "▾" else "▸", color = palette.smoke, fontSize = 12.sp)
                        }

                        if (open) {
                            if (b.readFirst.isNotEmpty()) {
                                Text("READ FIRST", color = palette.smoke, fontSize = 8.sp, fontWeight = FontWeight.Bold)
                                b.readFirst.forEach {
                                    Text(it, color = palette.text, fontSize = 10.sp, fontFamily = FontFamily.Monospace)
                                }
                            }
                            SelectionContainer {
                                Text(
                                    b.body.ifBlank { "(empty)" },
                                    color = palette.smoke, fontSize = 11.sp, lineHeight = 16.sp,
                                    fontFamily = FontFamily.Monospace
                                )
                            }
                            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                SmallButton("💬 Discuss / Deal", palette) { activeThread = "proj:${b.id}" to (b.title + ": " + b.summary) }
                                SmallButton("Copy for an AI", palette, filled = true) {
                                    val clipboard = context.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager
                                    clipboard.setPrimaryClip(ClipData.newPlainText(b.title, briefingText(b)))
                                    say("Copied “${b.title}”")
                                }
                                SmallButton("Edit", palette) { editorError = null; editor = EditorState(b) }
                                SmallButton(if (armedDeleteId == b.id) "Really delete?" else "Delete", palette, danger = true) {
                                    if (armedDeleteId != b.id) {
                                        armedDeleteId = b.id
                                    } else {
                                        armedDeleteId = null
                                        scope.launch {
                                            when (val r = deleteBriefing(serverHost, token, b.id)) {
                                                is Outcome.Ok -> { openId = null; say("Deleted “${b.title}”"); load() }
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

    editor?.let { state ->
        BriefingEditorDialog(
            palette = palette,
            initial = state.existing,
            saving = saving,
            error = editorError,
            onDismiss = { if (!saving) editor = null },
            onSave = { draft ->
                scope.launch {
                    saving = true
                    when (val r = saveBriefing(serverHost, token, state.existing?.id, draft)) {
                        is Outcome.Ok -> {
                            editor = null
                            openId = r.value.id
                            say("Saved “${r.value.title}”")
                            load()
                        }
                        is Outcome.Fail -> editorError = r.message
                    }
                    saving = false
                }
            }
        )
    }
}

@Composable
private fun BriefingEditorDialog(
    palette: DashboardPalette,
    initial: Briefing?,
    saving: Boolean,
    error: String?,
    onDismiss: () -> Unit,
    onSave: (Briefing) -> Unit
) {
    var title by rememberSaveable { mutableStateOf(initial?.title ?: "") }
    var project by rememberSaveable { mutableStateOf(initial?.project ?: "") }
    var summary by rememberSaveable { mutableStateOf(initial?.summary ?: "") }
    var readFirst by rememberSaveable { mutableStateOf(initial?.readFirst?.joinToString("\n") ?: "") }
    var body by rememberSaveable { mutableStateOf(initial?.body ?: "") }

    val colors = OutlinedTextFieldDefaults.colors(
        focusedBorderColor = palette.pink,
        unfocusedBorderColor = palette.smoke,
        focusedTextColor = palette.text,
        unfocusedTextColor = palette.text,
        focusedLabelColor = palette.glow,
        unfocusedLabelColor = palette.smoke,
        cursorColor = palette.pink
    )

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
                modifier = Modifier
                    .padding(14.dp)
                    .verticalScroll(rememberScrollState()),
                verticalArrangement = Arrangement.spacedBy(8.dp)
            ) {
                Text(
                    if (initial == null) "New briefing" else "Edit briefing",
                    color = palette.pink, fontSize = 14.sp, fontWeight = FontWeight.Bold
                )
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedTextField(
                        value = title, onValueChange = { title = it }, label = { Text("Title") },
                        singleLine = true, colors = colors, shape = RoundedCornerShape(10.dp),
                        modifier = Modifier.weight(3f)
                    )
                    OutlinedTextField(
                        value = project, onValueChange = { project = it }, label = { Text("Project") },
                        singleLine = true, colors = colors, shape = RoundedCornerShape(10.dp),
                        modifier = Modifier.weight(1.4f)
                    )
                }
                OutlinedTextField(
                    value = summary, onValueChange = { summary = it }, label = { Text("One-line summary (Lucille reads this)") },
                    maxLines = 3, colors = colors, shape = RoundedCornerShape(10.dp), modifier = Modifier.fillMaxWidth()
                )
                OutlinedTextField(
                    value = readFirst, onValueChange = { readFirst = it }, label = { Text("Read first (one path per line)") },
                    minLines = 2, maxLines = 5, colors = colors, shape = RoundedCornerShape(10.dp), modifier = Modifier.fillMaxWidth()
                )
                OutlinedTextField(
                    value = body, onValueChange = { body = it }, label = { Text("Briefing") },
                    minLines = 8, maxLines = 16, colors = colors, shape = RoundedCornerShape(10.dp), modifier = Modifier.fillMaxWidth()
                )
                error?.let { Text(it, color = palette.glow, fontSize = 11.sp) }
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                    SmallButton(if (saving) "Saving…" else "Save", palette, filled = true, enabled = !saving) {
                        onSave(
                            Briefing(
                                id = initial?.id ?: "",
                                title = title, project = project, summary = summary, body = body,
                                readFirst = readFirst.lines().map { it.trim() }.filter { it.isNotEmpty() }
                            )
                        )
                    }
                    SmallButton("Cancel", palette, enabled = !saving, onClick = onDismiss)
                }
            }
        }
    }
}

@Composable
private fun SmallButton(
    label: String,
    palette: DashboardPalette,
    filled: Boolean = false,
    danger: Boolean = false,
    enabled: Boolean = true,
    onClick: () -> Unit
) {
    val accent = if (danger) androidx.compose.ui.graphics.Color(0xFFFF4D6A) else palette.pink
    if (filled) {
        Button(
            onClick = onClick, enabled = enabled,
            colors = ButtonDefaults.buttonColors(containerColor = accent),
            contentPadding = PaddingValues(horizontal = 12.dp, vertical = 0.dp),
            modifier = Modifier.height(32.dp)
        ) { Text(label, fontSize = 11.sp, fontWeight = FontWeight.Bold) }
    } else {
        OutlinedButton(
            onClick = onClick, enabled = enabled,
            border = BorderStroke(1.dp, accent.copy(alpha = 0.7f)),
            contentPadding = PaddingValues(horizontal = 12.dp, vertical = 0.dp),
            modifier = Modifier.height(32.dp)
        ) { Text(label, color = if (danger) accent else palette.text, fontSize = 11.sp) }
    }
}
