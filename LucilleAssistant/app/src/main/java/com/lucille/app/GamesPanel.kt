package com.lucille.app

import android.content.Intent
import android.net.Uri
import android.widget.Toast
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyRow
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import kotlinx.coroutines.launch

// ─── Games ────────────────────────────────────────────────────────────────────────────────────
// Look up any game, open its card (description, reviews, web summary, prices, in-game screenshots you can enlarge),
// and mark it 👍 / 👎 / ♡. Marks are kept by Lucille on the PC (games.json), so this phone, the web page and the
// SWARM OS app share one wish list, and Lucille knows your taste when you talk to her.

private val Up = Color(0xFF4ADE80)
private val Down = Color(0xFFFF4433)
private val Mid = Color(0xFFFFC04D)

@Composable
fun GamesPanel(palette: DashboardPalette, serverHost: String, token: String) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var q by remember { mutableStateOf("") }
    var results by remember { mutableStateOf<List<GameHit>?>(null) }
    var searching by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var marks by remember { mutableStateOf<List<GameMark>?>(null) }
    var filter by remember { mutableStateOf("wish") }
    var card by remember { mutableStateOf<GameCardData?>(null) }
    var loadingCard by remember { mutableStateOf<String?>(null) }

    suspend fun loadMarks() {
        when (val r = fetchGameMarks(serverHost, token)) {
            is Outcome.Ok -> marks = r.value
            is Outcome.Fail -> error = r.message
        }
    }
    LaunchedEffect(serverHost, token) { loadMarks() }

    fun openGame(appid: Int, name: String) {
        loadingCard = name
        scope.launch {
            when (val r = fetchGame(serverHost, token, appid, name)) {
                is Outcome.Ok -> card = r.value
                is Outcome.Fail -> Toast.makeText(context, r.message, Toast.LENGTH_SHORT).show()
            }
            loadingCard = null
        }
    }

    fun search() {
        val query = q.trim()
        if (query.isEmpty() || searching) return
        searching = true; error = null; results = null
        scope.launch {
            when (val r = fetchGameSearch(serverHost, token, query)) {
                is Outcome.Ok -> results = r.value
                is Outcome.Fail -> error = r.message
            }
            searching = false
        }
    }

    Column(
        modifier = Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 10.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp)
    ) {
        Text("GAMES", color = palette.glow, fontSize = 13.sp, fontWeight = FontWeight.Bold, letterSpacing = 2.sp)

        Card(colors = CardDefaults.cardColors(containerColor = palette.panel), shape = RoundedCornerShape(14.dp), border = BorderStroke(1.dp, palette.pink.copy(alpha = 0.25f))) {
            Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("LOOK UP A GAME", color = palette.glow, fontSize = 10.sp, fontWeight = FontWeight.Bold, fontFamily = FontFamily.Monospace, letterSpacing = 1.5.sp)
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalAlignment = Alignment.CenterVertically) {
                    OutlinedTextField(
                        value = q, onValueChange = { q = it }, modifier = Modifier.weight(1f), singleLine = true,
                        placeholder = { Text("Hades, Elden Ring, Stardew…", color = palette.smoke, fontSize = 12.sp) },
                        textStyle = TextStyle(fontSize = 14.sp, color = palette.text),
                        keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
                        keyboardActions = KeyboardActions(onSearch = { search() })
                    )
                    TButton("Look up", palette) { search() }
                }
                if (searching) Text("Searching…", color = palette.smoke, fontSize = 12.sp)
                error?.let { Text(it, color = Down, fontSize = 12.sp) }
                results?.let { list ->
                    if (list.isEmpty()) Text("Steam has nothing by that name.", color = palette.smoke, fontSize = 12.sp)
                    list.forEach { g -> GameRow(g.name, g.image, listOf(g.price, if (g.metascore.isNotBlank()) "Metacritic ${g.metascore}" else "").filter { it.isNotBlank() }.joinToString(" · "), g.mark, palette) { openGame(g.appid, g.name) } }
                }
            }
        }

        Card(colors = CardDefaults.cardColors(containerColor = palette.panel), shape = RoundedCornerShape(14.dp), border = BorderStroke(1.dp, palette.pink.copy(alpha = 0.25f))) {
            Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("MY GAMES", color = palette.glow, fontSize = 10.sp, fontWeight = FontWeight.Bold, fontFamily = FontFamily.Monospace, letterSpacing = 1.5.sp)
                val all = marks.orEmpty()
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    listOf("wish" to "♡ Wish list", "like" to "👍 Liked", "dislike" to "👎 Disliked").forEach { (k, label) ->
                        val n = all.count { if (k == "wish") it.wish else it.like == k }
                        TButton("$label $n", palette) { filter = k }
                    }
                }
                val shown = all.filter { if (filter == "wish") it.wish else it.like == filter }
                if (marks == null) Text("Loading…", color = palette.smoke, fontSize = 12.sp)
                else if (shown.isEmpty()) Text("Nothing here yet. Open any game and press the buttons.", color = palette.smoke, fontSize = 12.sp)
                shown.forEach { m -> GameRow(m.name, m.image, "", m, palette) { openGame(m.appid, m.name) } }
            }
        }
        Spacer(Modifier.height(8.dp))
    }

    if (loadingCard != null && card == null) {
        Dialog(onDismissRequest = { loadingCard = null }) {
            Card(colors = CardDefaults.cardColors(containerColor = Color(0xFF1A0410))) {
                Text("Reading up on ${loadingCard}…", color = palette.text, modifier = Modifier.padding(20.dp), fontSize = 14.sp)
            }
        }
    }
    card?.let { c ->
        GameCardDialog(
            c, palette, serverHost, token,
            onMarkChanged = { m -> card = c.copy(mark = m); scope.launch { loadMarks() } },
            onClose = { card = null }
        )
    }
}

@Composable
internal fun GameRow(name: String, image: String, sub: String, mark: GameMark?, palette: DashboardPalette, onClick: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().background(Color(0x11FFFFFF), RoundedCornerShape(10.dp)).clickable { onClick() }.padding(8.dp),
        horizontalArrangement = Arrangement.spacedBy(10.dp), verticalAlignment = Alignment.CenterVertically
    ) {
        RemoteImage(image, Modifier.width(92.dp).height(35.dp).background(Color(0xFF111111), RoundedCornerShape(5.dp)), ContentScale.Crop)
        Column(Modifier.weight(1f)) {
            Text(name, color = palette.text, fontSize = 13.sp, fontWeight = FontWeight.SemiBold, maxLines = 1)
            if (sub.isNotBlank()) Text(sub, color = palette.smoke, fontSize = 11.sp)
        }
        val badge = (if (mark?.like == "like") "👍" else if (mark?.like == "dislike") "👎" else "") + if (mark?.wish == true) "♡" else ""
        if (badge.isNotEmpty()) Text(badge, fontSize = 15.sp)
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
internal fun GameCardDialog(
    c: GameCardData, palette: DashboardPalette, serverHost: String, token: String,
    onMarkChanged: (GameMark?) -> Unit, onClose: () -> Unit
) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var viewer by remember { mutableStateOf<Int?>(null) }
    var busy by remember { mutableStateOf(false) }
    val m = c.mark

    fun mark(like: String?, wish: Boolean?) {
        if (busy) return
        busy = true
        scope.launch {
            when (val r = markGame(serverHost, token, c.appid, c.name, c.image, like, wish)) {
                is Outcome.Ok -> onMarkChanged(r.value)
                is Outcome.Fail -> Toast.makeText(context, r.message, Toast.LENGTH_SHORT).show()
            }
            busy = false
        }
    }
    fun open(url: String) { if (url.startsWith("http")) context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url))) }

    Dialog(onDismissRequest = onClose, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Card(
            modifier = Modifier.fillMaxSize().padding(6.dp), shape = RoundedCornerShape(16.dp),
            colors = CardDefaults.cardColors(containerColor = Color(0xFF1A0410)), border = BorderStroke(1.dp, palette.pink.copy(alpha = 0.6f))
        ) {
            Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState())) {
                Box {
                    RemoteImage(c.image, Modifier.fillMaxWidth().heightIn(min = 80.dp, max = 200.dp), ContentScale.Crop)
                    Box(Modifier.align(Alignment.TopEnd).padding(8.dp)) { TButton("Close", palette) { onClose() } }
                }
                Column(Modifier.padding(14.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(c.name, color = palette.text, fontSize = 20.sp, fontWeight = FontWeight.Bold)
                    Text(listOf(c.release, c.developers.joinToString(", "), c.genres.joinToString(" · "), c.price).filter { it.isNotBlank() }.joinToString(" · "), color = palette.smoke, fontSize = 12.sp)

                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        TButton((if (m?.like == "like") "✓ " else "") + "👍 Like", palette) { mark(if (m?.like == "like") "" else "like", null) }
                        TButton((if (m?.like == "dislike") "✓ " else "") + "👎 Dislike", palette) { mark(if (m?.like == "dislike") "" else "dislike", null) }
                        TButton(if (m?.wish == true) "♥ On wish list" else "♡ Wish list", palette) { mark(null, m?.wish != true) }
                    }

                    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        if (c.metacritic > 0) TChip("Metacritic ${c.metacritic}", tTone(c.metacritic))
                        if (c.reviewWord.isNotBlank()) TChip("Steam: ${c.reviewWord} · ${c.reviewPct}%", tTone(c.reviewPct))
                    }

                    if (c.shots.isNotEmpty()) {
                        Section("IN-GAME SCREENSHOTS · TAP TO ENLARGE", palette)
                        LazyRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                            itemsIndexed(c.shots) { i, s ->
                                RemoteImage(s.thumb, Modifier.height(96.dp).width(170.dp).background(Color(0xFF111111), RoundedCornerShape(8.dp)).clickable { viewer = i }, ContentScale.Crop)
                            }
                        }
                    }
                    if (c.description.isNotBlank()) { Section("DESCRIPTION", palette); Text(c.description, color = palette.text, fontSize = 13.sp) }
                    if (c.samples.isNotEmpty()) {
                        Section("WHAT PLAYERS SAY", palette)
                        c.samples.forEach { r ->
                            Column(Modifier.fillMaxWidth().background((if (r.up) Up else Down).copy(alpha = 0.08f), RoundedCornerShape(8.dp)).padding(8.dp)) {
                                Text(r.text, color = palette.text, fontSize = 12.sp)
                                Text((if (r.up) "👍 recommended" else "👎 not recommended") + if (r.hours > 0) " · ${r.hours} h played" else "", color = palette.smoke, fontSize = 10.sp)
                            }
                        }
                    }
                    if (c.webExtract.isNotBlank()) {
                        Section("FROM THE WEB", palette)
                        Text(c.webExtract, color = palette.text, fontSize = 13.sp)
                        if (c.webUrl.isNotBlank()) Text("Read more on Wikipedia ↗", color = palette.glow, fontSize = 12.sp, modifier = Modifier.clickable { open(c.webUrl) })
                    }
                    if (c.searchUrl.isNotBlank()) Text("Search the whole web for reviews ↗", color = palette.glow, fontSize = 12.sp, modifier = Modifier.clickable { open(c.searchUrl) })
                    if (c.prices.isNotEmpty()) {
                        Section("WHERE TO BUY" + if (c.lowestEver.isNotBlank()) " · LOWEST EVER ${c.lowestEver}" else "", palette)
                        c.prices.take(6).forEach { p ->
                            Row(Modifier.fillMaxWidth().clickable { open(p.url) }.padding(vertical = 6.dp), horizontalArrangement = Arrangement.SpaceBetween) {
                                Text(p.store, color = palette.text, fontSize = 13.sp)
                                Text("$" + p.price + if (p.savings > 0) "  -${p.savings}%" else "", color = palette.glow, fontSize = 13.sp, fontWeight = FontWeight.Bold)
                            }
                        }
                    } else if (c.storeUrl.isNotBlank()) Text("Open the Steam page ↗", color = palette.glow, fontSize = 12.sp, modifier = Modifier.clickable { open(c.storeUrl) })
                    if (c.notes.isNotEmpty()) Text("Couldn't reach: " + c.notes.joinToString(", ") { it.substringBefore(":") } + ".", color = palette.smoke, fontSize = 10.sp)
                }
            }
        }
    }

    viewer?.let { start ->
        Dialog(onDismissRequest = { viewer = null }, properties = DialogProperties(usePlatformDefaultWidth = false)) {
            val pager = rememberPagerState(initialPage = start) { c.shots.size }
            Box(Modifier.fillMaxSize().background(Color.Black)) {
                HorizontalPager(state = pager, modifier = Modifier.fillMaxSize()) { page ->
                    RemoteImage(c.shots[page].full, Modifier.fillMaxSize(), ContentScale.Fit)
                }
                Text("${pager.currentPage + 1} / ${c.shots.size}", color = Color.White, fontSize = 12.sp, fontFamily = FontFamily.Monospace, modifier = Modifier.align(Alignment.BottomCenter).padding(16.dp))
                Box(Modifier.align(Alignment.TopEnd).padding(12.dp)) { TButton("✕", palette) { viewer = null } }
            }
        }
    }
}

@Composable
private fun Section(title: String, palette: DashboardPalette) {
    Text(title, color = palette.glow, fontSize = 10.sp, fontWeight = FontWeight.Bold, fontFamily = FontFamily.Monospace, letterSpacing = 1.5.sp, modifier = Modifier.padding(top = 6.dp))
}

private fun tTone(pct: Int): Color = if (pct >= 75) Up else if (pct >= 50) Mid else Down
