package com.lucille.app

import androidx.activity.compose.BackHandler
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.slideOutVertically
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.gestures.detectVerticalDragGestures
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.launch

// ─── Game Mode: black / orange / green — the branding SWARM OS's gaming agent uses ──
// Entered by a single swipe in from the left edge. Separate from Lucille's own pink/blue
// persona palette on purpose: this is the SWARM gaming brand, not a Lucille mood.

private val GameModeBg = Color(0xFF070908)
private val GameModePanel = Color(0xFF0D1210)
private val GameModeOrange = Color(0xFFFF7A29)
private val GameModeGreen = Color(0xFF39D98A)
private val GameModeText = Color(0xFFEEF1EE)
private val GameModeMuted = Color(0xFF9AA39C)

private const val SWIPE_RIGHT_THRESHOLD_DP = 120f
private const val SWIPE_DOWN_CLOSE_THRESHOLD_DP = 140f
private const val EDGE_STRIP_WIDTH_DP = 24f

/**
 * A thin strip on the left edge watches for a single rightward swipe and opens Game Mode
 * full-screen. Swipe down, tap Close, or Back leaves it. The rest of the dashboard's own
 * gestures and dock are untouched.
 */
@Composable
fun GameModeOverlay(open: Boolean, onOpenChange: (Boolean) -> Unit, palette: DashboardPalette, serverHost: String, token: String) {
    BackHandler(enabled = open) { onOpenChange(false) }

    Box(modifier = Modifier.fillMaxSize()) {
        if (!open) {
            var totalDx by remember { mutableStateOf(0f) }
            Box(
                modifier = Modifier
                    .align(Alignment.CenterStart)
                    .fillMaxHeight()
                    .width(EDGE_STRIP_WIDTH_DP.dp)
                    .pointerInput(Unit) {
                        val thresholdPx = SWIPE_RIGHT_THRESHOLD_DP.dp.toPx()
                        detectHorizontalDragGestures(
                            onDragStart = { totalDx = 0f },
                            onDragCancel = { totalDx = 0f },
                            onDragEnd = {
                                if (totalDx >= thresholdPx) {
                                    onOpenChange(true)
                                }
                                totalDx = 0f
                            },
                            onHorizontalDrag = { change, dx -> change.consume(); totalDx += dx }
                        )
                    }
            )
        }

        AnimatedVisibility(visible = open, enter = fadeIn(), exit = fadeOut()) {
            Box(modifier = Modifier.fillMaxSize().background(Color.Black.copy(alpha = 0.6f)))
        }
        AnimatedVisibility(
            visible = open,
            enter = slideInVertically(initialOffsetY = { it }),
            exit = slideOutVertically(targetOffsetY = { it })
        ) {
            GameModeScreen(onClose = { onOpenChange(false) }, palette = palette, serverHost = serverHost, token = token)
        }
    }
}

@Composable
private fun GameModeScreen(onClose: () -> Unit, palette: DashboardPalette, serverHost: String, token: String) {
    val scope = rememberCoroutineScope()
    var totalDy by remember { mutableStateOf(0f) }
    var deals by remember { mutableStateOf<List<GameDeal>?>(null) }
    var dealsError by remember { mutableStateOf<String?>(null) }
    var stats by remember { mutableStateOf<GamingStats?>(null) }
    var statsError by remember { mutableStateOf<String?>(null) }
    var card by remember { mutableStateOf<GameCardData?>(null) }
    var loadingCard by remember { mutableStateOf<String?>(null) }
    var cardError by remember { mutableStateOf<String?>(null) }

    // Refetched every time Game Mode opens (this composable is only alive while open), so the
    // list is never stuck showing the same handful of games for days.
    LaunchedEffect(Unit) {
        when (val r = fetchDeals(serverHost, token)) {
            // Shuffled once per open (not per recomposition), inside each group so Steam stays
            // first: the backend's "Deal Rating" sort barely moves day to day, so always showing
            // its literal top 6 looked frozen even once this was wired to live data.
            is Outcome.Ok -> deals = r.value.let { all -> all.filter { it.store == "Steam" }.shuffled() + all.filter { it.store != "Steam" }.shuffled() }
            is Outcome.Fail -> dealsError = r.message
        }
        when (val r = fetchGamingStats(serverHost, token)) {
            is Outcome.Ok -> stats = r.value
            is Outcome.Fail -> statsError = r.message
        }
    }

    fun openGame(appid: Int, name: String) {
        loadingCard = name
        cardError = null
        scope.launch {
            when (val r = fetchGame(serverHost, token, appid, name)) {
                is Outcome.Ok -> card = r.value
                // Its own line under the list: one game that can't be looked up must not hide the rest.
                is Outcome.Fail -> cardError = "$name: ${r.message}"
            }
            loadingCard = null
        }
    }
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(GameModeBg)
            .statusBarsPadding()
            .navigationBarsPadding()
            .pointerInput(Unit) {
                val closePx = SWIPE_DOWN_CLOSE_THRESHOLD_DP.dp.toPx()
                detectVerticalDragGestures(
                    onDragStart = { totalDy = 0f },
                    onDragCancel = { totalDy = 0f },
                    onDragEnd = {
                        if (totalDy > closePx) onClose()
                        totalDy = 0f
                    },
                    onVerticalDrag = { change, dy -> change.consume(); totalDy += dy }
                )
            }
    ) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(16.dp)
                .verticalScroll(rememberScrollState()),
            verticalArrangement = Arrangement.spacedBy(14.dp)
        ) {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically
            ) {
                Text("GAME MODE", color = GameModeOrange, fontSize = 22.sp, fontWeight = FontWeight.Bold, letterSpacing = 1.sp)
                Button(
                    onClick = onClose,
                    colors = ButtonDefaults.buttonColors(containerColor = GameModePanel),
                    border = BorderStroke(1.dp, GameModeGreen.copy(alpha = 0.5f)),
                    shape = RoundedCornerShape(8.dp)
                ) { Text("Close ✕", color = GameModeGreen, fontSize = 12.sp) }
            }
            Text("Swipe down to exit", color = GameModeMuted, fontSize = 11.sp)

            Card(
                colors = CardDefaults.cardColors(containerColor = GameModePanel),
                shape = RoundedCornerShape(16.dp),
                border = BorderStroke(1.dp, GameModeOrange.copy(alpha = 0.4f))
            ) {
                Column(modifier = Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text("⚡ SWARM Gaming", color = GameModeOrange, fontSize = 14.sp, fontWeight = FontWeight.Bold)
                    // Only says what was actually checked: Lucille on the PC answering, and Steam's
                    // own reply through her (she calls the Steam Web API directly, not GamingAgent).
                    val steamState = when {
                        statsError != null -> Pair("PC unreachable", false)
                        stats == null -> Pair("Checking…", true)
                        stats!!.steamError.isNotBlank() -> Pair(stats!!.steamError, false)
                        else -> Pair("Connected", true)
                    }
                    StatLine("Steam, via Lucille on the PC", steamState.first, steamState.second)
                    if (stats != null && stats!!.steamError.isBlank()) {
                        StatLine("Steam hours (2 weeks)", if (stats!!.steamHours2Weeks > 0) "%.1f hrs".format(stats!!.steamHours2Weeks) else "None played", true)
                    }
                    if (stats != null) {
                        StatLine("Logged by you this week", "%.1f hrs".format(stats!!.loggedWeekHours), true)
                    }
                }
            }

            Card(
                colors = CardDefaults.cardColors(containerColor = GameModePanel),
                shape = RoundedCornerShape(16.dp),
                border = BorderStroke(1.dp, GameModeGreen.copy(alpha = 0.4f))
            ) {
                Column(modifier = Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    Text("🎮 Curated Deals", color = GameModeGreen, fontSize = 14.sp, fontWeight = FontWeight.Bold)
                    when {
                        dealsError != null -> Text(dealsError!!, color = Color(0xFFFF4433), fontSize = 12.sp)
                        deals == null -> Text("Loading today's deals…", color = GameModeMuted, fontSize = 12.sp)
                        deals!!.isEmpty() -> Text("No deals worth showing right now.", color = GameModeMuted, fontSize = 12.sp)
                        else -> deals!!.take(6).forEach { d ->
                            GameRow(
                                d.title, d.thumb,
                                listOf("$${d.salePrice}", "-${d.savingsPct}%", d.store, if (d.ratingPct.isNotBlank() && d.ratingPct != "0") "${d.ratingPct}% positive" else "")
                                    .filter { it.isNotBlank() }.joinToString(" · "),
                                null, palette
                            ) { openGame(d.steamAppId, d.title) }
                        }
                    }
                    cardError?.let { Text(it, color = Color(0xFFFF4433), fontSize = 11.sp) }
                }
            }
        }
    }

    if (loadingCard != null && card == null) {
        Box(Modifier.fillMaxSize().background(Color.Black.copy(alpha = 0.5f)), contentAlignment = Alignment.Center) {
            Card(colors = CardDefaults.cardColors(containerColor = GameModePanel), shape = RoundedCornerShape(12.dp)) {
                Text("Reading up on $loadingCard…", color = GameModeText, modifier = Modifier.padding(20.dp), fontSize = 14.sp)
            }
        }
    }
    card?.let { c ->
        GameCardDialog(c, palette, serverHost, token, onMarkChanged = { card = c.copy(mark = it) }, onClose = { card = null })
    }
}

@Composable
private fun StatLine(label: String, value: String, good: Boolean) {
    Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
        Text(label, color = GameModeText, fontSize = 12.sp)
        Spacer(Modifier.width(12.dp))
        Text(value, color = if (good) GameModeGreen else Color(0xFFFF4433), fontSize = 11.sp)
    }
}
