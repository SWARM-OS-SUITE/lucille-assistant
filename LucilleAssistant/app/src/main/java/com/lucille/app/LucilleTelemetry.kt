package com.lucille.app

import android.graphics.Rect
import android.os.Build
import android.util.Log
import androidx.activity.compose.BackHandler
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInHorizontally
import androidx.compose.animation.slideOutHorizontally
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.boundsInWindow
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import kotlinx.coroutines.flow.flowOn
import kotlinx.coroutines.launch
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL

// ─── Telemetry for every node ────────────────────────────────────────────────
// The PC (Lucille Assistant 1.2+) streams its own numbers and those of every node it is
// paired with over one SSE connection, so the phone needs a single link and no node tokens.
// A value of -1 means "could not be measured" and is shown as a dash, never as zero.

data class NodeTelemetry(
    val hostname: String,
    val os: String,
    val cpu: Double,
    val ram: Double,
    val ramTotalMb: Long,
    val ramUsedMb: Long,
    val storageTotalGb: Double,
    val storageFreeGb: Double,
    val load1: Double,
    val uptimeS: Long,
    val tempC: Double?,
    val swap: Double,
    val rxKbps: Double,
    val txKbps: Double,
    val cores: Int
)

data class NodeView(
    val name: String,
    val isLocal: Boolean,
    val online: Boolean,
    val lastSeenMs: Long?,
    val error: String,
    val t: NodeTelemetry?
)

sealed class TelemetryState {
    object Connecting : TelemetryState()
    data class Live(val nodes: List<NodeView>, val receivedAtMs: Long) : TelemetryState()
    data class Down(val message: String, val last: List<NodeView>) : TelemetryState()
}

private fun JSONObject.num(key: String): Double = if (has(key) && !isNull(key)) optDouble(key, -1.0) else -1.0

private fun JSONObject.toTelemetry() = NodeTelemetry(
    hostname = optString("hostname"),
    os = optString("os"),
    cpu = num("cpu_percent"),
    ram = num("ram_percent"),
    ramTotalMb = num("ram_total_mb").toLong(),
    ramUsedMb = num("ram_used_mb").toLong(),
    storageTotalGb = num("storage_total_gb"),
    storageFreeGb = num("storage_free_gb"),
    load1 = num("load1"),
    uptimeS = num("uptime_seconds").toLong(),
    tempC = if (has("temp_c") && !isNull("temp_c")) optDouble("temp_c") else null,
    swap = num("swap_percent"),
    rxKbps = num("net_rx_kbps"),
    txKbps = num("net_tx_kbps"),
    cores = optInt("cores", 0)
)

internal fun parseNodes(json: String): List<NodeView> {
    val arr = JSONObject(json).optJSONArray("nodes") ?: return emptyList()
    return List(arr.length()) { i ->
        val n = arr.getJSONObject(i)
        NodeView(
            name = n.optString("name"),
            isLocal = n.optString("kind") == "local",
            online = n.optBoolean("online"),
            lastSeenMs = n.optString("last_seen").takeIf { it.isNotBlank() }?.let { runCatching { java.time.Instant.parse(it).toEpochMilli() }.getOrNull() },
            error = n.optString("error"),
            t = n.optJSONObject("telemetry")?.toTelemetry()
        )
    }
}

/**
 * Follows the PC's telemetry stream, trying the usual PC addresses and reconnecting with a growing pause.
 * Collect it only while the drawer is open: leaving the collection closes the connection.
 */
fun telemetryFlow(serverBase: String, token: String): Flow<TelemetryState> = flow {
    var last: List<NodeView> = emptyList()
    var pause = 1000L
    emit(TelemetryState.Connecting)
    while (true) {
        currentCoroutineContext().ensureActive()
        var failure = "Can't reach your PC. Check the address in Config and that Lucille Assistant is running."
        for (base in lucilleBases(serverBase)) {
            val conn = try {
                (URL("$base/api/telemetry/stream").openConnection() as HttpURLConnection).apply {
                    connectTimeout = 3500
                    readTimeout = 20000 // the PC sends something at least every 15 s
                    if (token.isNotBlank()) setRequestProperty("X-Swarm-Auth", token)
                    setRequestProperty("Accept", "text/event-stream")
                }
            } catch (e: Exception) {
                continue
            }
            try {
                val code = conn.responseCode
                if (code == 404) {
                    failure = "Your PC is running an older Lucille Assistant with no telemetry. Update it to 1.2 or newer."
                    break // it answered, so the other addresses are the same PC
                }
                if (code != 200) {
                    failure = if (code == 401) "The PC refused this app's token." else "The PC answered HTTP $code."
                    break
                }
                var event = ""
                val data = StringBuilder()
                conn.inputStream.bufferedReader().use { reader ->
                    while (true) {
                        currentCoroutineContext().ensureActive()
                        val line = reader.readLine() ?: break
                        when {
                            line.isEmpty() -> {
                                if (event == "nodes" && data.isNotEmpty()) {
                                    last = parseNodes(data.toString())
                                    pause = 1000L
                                    emit(TelemetryState.Live(last, System.currentTimeMillis()))
                                }
                                event = ""
                                data.clear()
                            }
                            line.startsWith("event:") -> event = line.removePrefix("event:").trim()
                            line.startsWith("data:") -> {
                                if (data.isNotEmpty()) data.append('\n')
                                data.append(line.removePrefix("data:").removePrefix(" "))
                            }
                        }
                    }
                }
                failure = "The stream from your PC ended."
                break
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (e: Exception) {
                Log.w("LucilleTelemetry", "$base: ${e.message}")
            } finally {
                conn.disconnect()
            }
        }
        emit(TelemetryState.Down(failure, last))
        delay(pause)
        pause = (pause * 2).coerceAtMost(10_000L)
    }
}.flowOn(Dispatchers.IO)

// ─── Formatting ──────────────────────────────────────────────────────────────

private val OkGreen = Color(0xFF3DDC84)
private val WarnAmber = Color(0xFFFFB84D)
private val HotRed = Color(0xFFFF4D6A)

internal fun pct(v: Double): String = if (v < 0) "—" else "${Math.round(v)}%"

internal fun uptimeText(s: Long): String = when {
    s < 0 -> "—"
    s >= 86_400 -> "${s / 86_400}d ${(s % 86_400) / 3600}h"
    s >= 3600 -> "${s / 3600}h ${(s % 3600) / 60}m"
    else -> "${s / 60}m"
}

internal fun rateText(kbps: Double): String = when {
    kbps < 0 -> "—"
    kbps >= 1024 -> String.format("%.1f MB/s", kbps / 1024)
    else -> String.format("%.0f KB/s", kbps)
}

private fun levelColor(v: Double): Color = when {
    v >= 92 -> HotRed
    v >= 80 -> WarnAmber
    else -> OkGreen
}

private fun agoText(ms: Long?): String {
    if (ms == null) return "never"
    val s = (System.currentTimeMillis() - ms) / 1000
    return when {
        s < 60 -> "${s}s ago"
        s < 3600 -> "${s / 60}m ago"
        else -> "${s / 3600}h ago"
    }
}

// ─── Swipe-from-the-right drawer ─────────────────────────────────────────────

/**
 * Asks Android to leave this area to the app instead of treating a swipe from the screen edge as Back.
 * The system honours at most 200 dp of height per edge, so the slice is centred on the strip.
 */
@Composable
private fun Modifier.excludeSystemBackGesture(): Modifier {
    val view = LocalView.current
    val density = LocalDensity.current
    DisposableEffect(view) {
        onDispose { if (Build.VERSION.SDK_INT >= 29) view.systemGestureExclusionRects = emptyList() }
    }
    return this.onGloballyPositioned { c ->
        if (Build.VERSION.SDK_INT >= 29) {
            val b = c.boundsInWindow()
            val h = minOf(b.height, with(density) { 200.dp.toPx() })
            val top = b.top + (b.height - h) / 2
            view.systemGestureExclusionRects = listOf(Rect(b.left.toInt(), top.toInt(), b.right.toInt(), (top + h).toInt()))
        }
    }
}

/**
 * A thin strip on the right edge opens the drawer when dragged left (a small pull tab shows where,
 * and tapping it works too). It asks Android not to treat that edge as the Back gesture.
 */
@Composable
fun TelemetryOverlay(palette: DashboardPalette, serverHost: String, token: String, open: Boolean, onOpenChange: (Boolean) -> Unit) {
    BackHandler(enabled = open) { onOpenChange(false) }
    Box(modifier = Modifier.fillMaxSize()) {
        if (!open) {
            Box(
                modifier = Modifier
                    .align(Alignment.CenterEnd)
                    .fillMaxHeight(0.6f)
                    .width(36.dp)
                    .excludeSystemBackGesture()
                    .pointerInput(Unit) {
                        var total = 0f
                        detectHorizontalDragGestures(
                            onDragStart = { total = 0f },
                            onDragEnd = { total = 0f },
                            onDragCancel = { total = 0f },
                            onHorizontalDrag = { change, dx ->
                                change.consume()
                                total += dx
                                if (total < -50f) { total = 0f; onOpenChange(true) }
                            }
                        )
                    }
            ) {
                Box(
                    modifier = Modifier
                        .align(Alignment.CenterEnd)
                        .size(width = 16.dp, height = 72.dp)
                        .clip(RoundedCornerShape(topStart = 12.dp, bottomStart = 12.dp))
                        .background(palette.panel)
                        .clickable { onOpenChange(true) },
                    contentAlignment = Alignment.Center
                ) {
                    Text("‹", color = palette.glow, fontSize = 18.sp, fontWeight = FontWeight.Bold)
                }
            }
        }

        AnimatedVisibility(visible = open, enter = fadeIn(), exit = fadeOut()) {
            Box(
                modifier = Modifier
                    .fillMaxSize()
                    .background(Color.Black.copy(alpha = 0.5f))
                    .clickable(interactionSource = remember { MutableInteractionSource() }, indication = null) { onOpenChange(false) }
            )
        }
        AnimatedVisibility(
            visible = open,
            enter = slideInHorizontally { it },
            exit = slideOutHorizontally { it },
            modifier = Modifier.align(Alignment.CenterEnd)
        ) {
            TelemetryPanel(palette, serverHost, token, onClose = { onOpenChange(false) })
        }
    }
}

@Composable
private fun TelemetryPanel(palette: DashboardPalette, serverHost: String, token: String, onClose: () -> Unit) {
    // Collected only while this panel is composed, so the stream runs only while the drawer is open.
    val state by remember(serverHost, token) { telemetryFlow(serverHost, token) }.collectAsState(initial = TelemetryState.Connecting)

    Card(
        colors = CardDefaults.cardColors(containerColor = palette.panel.copy(alpha = 0.97f)),
        shape = RoundedCornerShape(topStart = 20.dp, bottomStart = 20.dp),
        border = BorderStroke(1.dp, palette.pink.copy(alpha = 0.5f)),
        modifier = Modifier
            .fillMaxHeight()
            .widthIn(max = 380.dp)
            .fillMaxWidth(0.9f)
            .statusBarsPadding()
            .navigationBarsPadding()
            .pointerInput(Unit) {
                var total = 0f
                detectHorizontalDragGestures(
                    onDragStart = { total = 0f },
                    onDragEnd = { total = 0f },
                    onDragCancel = { total = 0f },
                    onHorizontalDrag = { change, dx ->
                        change.consume()
                        total += dx
                        if (total > 90f) { total = 0f; onClose() }
                    }
                )
            }
    ) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .verticalScroll(rememberScrollState())
                .padding(14.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp)
        ) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.SpaceBetween, modifier = Modifier.fillMaxWidth()) {
                Text("📡 Telemetry", color = palette.pink, fontSize = 15.sp, fontWeight = FontWeight.Bold)
                val (label, color) = when (val s = state) {
                    is TelemetryState.Live -> "● live" to OkGreen
                    is TelemetryState.Connecting -> "connecting…" to palette.smoke
                    is TelemetryState.Down -> "● offline" to HotRed
                }
                Text(label, color = color, fontSize = 10.sp, fontFamily = FontFamily.Monospace)
            }

            val nodes = when (val s = state) {
                is TelemetryState.Live -> s.nodes
                is TelemetryState.Down -> s.last
                else -> emptyList()
            }
            (state as? TelemetryState.Down)?.let {
                Text(it.message, color = palette.glow, fontSize = 11.sp)
            }
            nodes.forEach { NodeCard(it, palette) }
            if (state is TelemetryState.Live && nodes.count { !it.isLocal } == 0) {
                Text(
                    "Only this PC so far. To add another machine, run on the PC:\nlucille-assistant nodes add NAME http://<node-ip>:7437 CODE\n(CODE comes from `i30ltz-node pair` on that machine.)",
                    color = palette.smoke, fontSize = 10.sp, fontFamily = FontFamily.Monospace
                )
            }
            HorizontalDivider(color = palette.pink.copy(alpha = 0.2f))
            ActionsSection(palette, serverHost, token, live = state is TelemetryState.Live)
            Text("Swipe right or tap outside to close.", color = palette.smoke.copy(alpha = 0.7f), fontSize = 9.sp)
        }
    }
}

@Composable
private fun NodeCard(n: NodeView, palette: DashboardPalette) {
    Card(
        colors = CardDefaults.cardColors(containerColor = palette.panel),
        shape = RoundedCornerShape(14.dp),
        border = BorderStroke(1.dp, (if (n.online) OkGreen else palette.smoke).copy(alpha = 0.35f))
    ) {
        Column(modifier = Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Box(modifier = Modifier.size(9.dp).clip(CircleShape).background(if (n.online) OkGreen else HotRed))
                Text(n.name, color = palette.glow, fontSize = 13.sp, fontWeight = FontWeight.Bold, modifier = Modifier.weight(1f))
                Text(if (n.isLocal) "this PC" else "node", color = palette.smoke, fontSize = 9.sp)
            }
            val t = n.t
            if (t == null) {
                Text(n.error.ifBlank { "No data yet." }, color = palette.smoke, fontSize = 10.sp)
                return@Column
            }
            if (t.os.isNotBlank()) Text(t.os + if (t.cores > 0) "  ·  ${t.cores} cores" else "", color = palette.smoke, fontSize = 9.sp)
            Meter("CPU", t.cpu, pct(t.cpu), palette)
            Meter("RAM", t.ram, pct(t.ram) + if (t.ramTotalMb > 0) "  ${t.ramUsedMb} / ${t.ramTotalMb} MB" else "", palette)
            if (t.swap >= 0) Meter("Swap", t.swap, pct(t.swap), palette)
            if (t.storageTotalGb > 0 && t.storageFreeGb >= 0) {
                val used = (1 - t.storageFreeGb / t.storageTotalGb) * 100
                Meter("Disk", used, "${pct(used)}  ${Math.round(t.storageFreeGb)} GB free", palette)
            }
            Row(horizontalArrangement = Arrangement.spacedBy(14.dp), modifier = Modifier.padding(top = 2.dp)) {
                Stat("load", if (t.load1 >= 0) String.format("%.2f", t.load1) else "—", palette)
                Stat("temp", t.tempC?.let { "${Math.round(it)}°C" } ?: "—", palette)
                Stat("up", uptimeText(t.uptimeS), palette)
            }
            if (t.rxKbps >= 0 || t.txKbps >= 0) {
                Text("↓ ${rateText(t.rxKbps)}   ↑ ${rateText(t.txKbps)}", color = palette.text, fontSize = 10.sp, fontFamily = FontFamily.Monospace)
            }
            if (!n.online) {
                Text("Offline · last seen ${agoText(n.lastSeenMs)}" + if (n.error.isNotBlank()) " · ${n.error}" else "", color = HotRed, fontSize = 10.sp)
            }
        }
    }
}

@Composable
private fun Meter(label: String, value: Double, text: String, palette: DashboardPalette) {
    Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
            Text(label, color = palette.smoke, fontSize = 10.sp)
            Text(text, color = palette.text, fontSize = 10.sp, fontFamily = FontFamily.Monospace)
        }
        LinearProgressIndicator(
            progress = { if (value < 0) 0f else (value / 100.0).coerceIn(0.0, 1.0).toFloat() },
            color = if (value < 0) palette.smoke else levelColor(value),
            trackColor = palette.smoke.copy(alpha = 0.18f),
            gapSize = 0.dp,
            drawStopIndicator = {},
            modifier = Modifier.fillMaxWidth().height(5.dp).clip(RoundedCornerShape(3.dp))
        )
    }
}

@Composable
private fun Stat(label: String, value: String, palette: DashboardPalette) {
    Column {
        Text(label, color = palette.smoke, fontSize = 8.sp)
        Text(value, color = palette.text, fontSize = 12.sp, fontWeight = FontWeight.Bold, fontFamily = FontFamily.Monospace)
    }
}

// ─── PC actions ───────────────────────────────────────────────────────────────
// A fixed allowlist the PC itself defines (config.json, edited by hand there, never
// over the network) — never an arbitrary command from the phone. A destructive one
// needs a second tap, matching the briefing-delete pattern elsewhere in the app.

@Composable
private fun ActionsSection(palette: DashboardPalette, serverHost: String, token: String, live: Boolean) {
    val scope = rememberCoroutineScope()
    var actions by remember { mutableStateOf<List<ActionItem>?>(null) }
    var loadError by remember { mutableStateOf<String?>(null) }
    var armedId by remember { mutableStateOf("") }
    var busyId by remember { mutableStateOf("") }
    var lastResult by remember { mutableStateOf<Pair<ActionItem, ActionRunResult>?>(null) }

    LaunchedEffect(serverHost, token, live) {
        if (!live) return@LaunchedEffect
        when (val r = fetchActions(serverHost, token)) {
            is Outcome.Ok -> { actions = r.value; loadError = null }
            is Outcome.Fail -> loadError = r.message
        }
    }

    Text("⚙️ PC actions", color = palette.pink, fontSize = 13.sp, fontWeight = FontWeight.Bold)
    Text(
        "A fixed allowlist run on the PC itself — never an arbitrary command. Destructive ones need a second tap.",
        color = palette.smoke, fontSize = 10.sp
    )

    if (!live) {
        Text("Connect to your PC to see and run actions.", color = palette.smoke, fontSize = 11.sp)
        return
    }
    loadError?.let { Text(it, color = palette.glow, fontSize = 11.sp) }

    actions?.forEach { a ->
        val armed = armedId == a.id
        val busy = busyId == a.id
        Card(
            colors = CardDefaults.cardColors(containerColor = palette.panel),
            shape = RoundedCornerShape(12.dp),
            border = BorderStroke(1.dp, (if (a.destructive) HotRed else palette.pink).copy(alpha = 0.3f))
        ) {
            Row(
                modifier = Modifier.padding(10.dp).fillMaxWidth(),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(10.dp)
            ) {
                Column(modifier = Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(2.dp)) {
                    Text(a.label, color = palette.glow, fontSize = 12.sp, fontWeight = FontWeight.Bold)
                    Text(a.description, color = palette.smoke, fontSize = 10.sp)
                    if (a.sudo) Text("needs sudo on the PC", color = palette.smoke.copy(alpha = 0.7f), fontSize = 9.sp)
                }
                ActionButton(
                    label = if (busy) "…" else if (a.destructive && armed) "Really?" else "Run",
                    danger = a.destructive,
                    enabled = busyId.isEmpty(),
                    onClick = {
                        if (a.destructive && !armed) {
                            armedId = a.id
                            scope.launch { delay(4000); if (armedId == a.id) armedId = "" }
                        } else {
                            armedId = ""
                            busyId = a.id
                            scope.launch {
                                val outcome = runPcAction(serverHost, token, a.id, confirm = true)
                                lastResult = a to when (outcome) {
                                    is Outcome.Ok -> outcome.value
                                    is Outcome.Fail -> ActionRunResult(ok = false, needsConfirm = false, exitCode = null, output = "", error = outcome.message)
                                }
                                busyId = ""
                            }
                        }
                    }
                )
            }
        }
    }

    lastResult?.let { (a, r) ->
        val status = when {
            r.needsConfirm -> "needs confirmation"
            r.ok -> "done"
            else -> "failed"
        }
        Text(
            "${a.label} — $status" + (r.exitCode?.let { " (exit $it)" } ?: ""),
            color = if (r.ok) OkGreen else if (r.needsConfirm) WarnAmber else HotRed,
            fontSize = 11.sp, fontWeight = FontWeight.Bold
        )
        val shown = r.output.ifBlank { r.error }
        if (shown.isNotBlank()) {
            Text(
                shown.take(4000), color = palette.text, fontSize = 10.sp, fontFamily = FontFamily.Monospace,
                modifier = Modifier.heightIn(max = 160.dp).verticalScroll(rememberScrollState())
            )
        }
    }
}

@Composable
private fun ActionButton(label: String, danger: Boolean, enabled: Boolean, onClick: () -> Unit) {
    val color = if (danger) HotRed else OkGreen
    Button(
        onClick = onClick,
        enabled = enabled,
        colors = ButtonDefaults.buttonColors(containerColor = color.copy(alpha = if (enabled) 1f else 0.4f)),
        contentPadding = PaddingValues(horizontal = 14.dp, vertical = 0.dp),
        modifier = Modifier.height(30.dp).widthIn(min = 64.dp)
    ) {
        Text(label, fontSize = 11.sp, fontWeight = FontWeight.Bold, color = Color.Black)
    }
}
