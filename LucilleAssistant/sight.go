package main

// Lucille looks at the page you are on (user 2026-10-14: "when I'm on the web I want her to see what I see and help
// where she can"). The Lucille Sight browser extension (extension/) sends the open tab to POST /api/sight ONLY when the
// user presses its button or hotkey; it never reads in the background (activeTab permission). Lucille answers as
// herself, with the page in front of her. On a Steam store page she also pulls the game card.
//
// Safety rules, all enforced here and not only in the extension:
//   - the caller must carry a paired-device token. A web page in the browser cannot call this, even on loopback.
//   - pages that look like banking, payments, mail, health, taxes, passwords, logins or checkouts are refused outright.
//   - secret-looking strings and long digit runs (card, account, SSN shapes) are redacted before anything is sent on.
//   - nothing is stored: not the URL, not the text, not the answer.
//   - "keep it on this PC" makes the answer come from the offline models only; otherwise the text goes to Lucille's
//     cloud model, exactly like a chat message would.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	sightMaxText = 9000
	sightMaxSel  = 2000
	sightMaxQ    = 600
)

// Sites and paths she never looks at. Substring match on the host, or on the path for the second list.
var (
	sightBlockHosts = []string{
		"bank", "chase.com", "wellsfargo", "citi.com", "capitalone", "usaa.com", "schwab", "fidelity.com", "vanguard", "etrade", "robinhood",
		"paypal", "venmo", "cash.app", "zelle", "stripe.com", "coinbase", "binance", "kraken.com", "creditkarma", "experian", "equifax", "transunion",
		"irs.gov", "ssa.gov", "healthcare.gov", "medicare.gov", "mychart", "patient", "myhealth", "kaiser", "anthem", "cigna", "aetna", "va.gov",
		"mail.google", "gmail", "outlook", "office365", "protonmail", "proton.me", "yahoo.com/mail", "icloud.com",
		"accounts.google", "login.", "signin.", "auth.", "sso.", "1password", "bitwarden", "lastpass", "dashlane", "keepass",
	}
	sightBlockPaths = []string{"/login", "/signin", "/sign-in", "/log-in", "/checkout", "/payment", "/billing", "/cart/checkout", "/account/security", "/password", "/oauth", "/2fa", "/mfa"}

	digitRun    = regexp.MustCompile(`\b(?:\d[ -]?){12,19}\b|\b\d{3}-\d{2}-\d{4}\b`)
	sightExtraB = os.Getenv("LUCILLE_SIGHT_BLOCK") // comma-separated extra host fragments
)

// sightBlocked says why a page is off limits ("" = fine).
func sightBlocked(u *url.URL) string {
	host, path := strings.ToLower(u.Hostname()), strings.ToLower(u.Path)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "I can only look at web pages."
	}
	if host == "" || host == "localhost" || strings.HasPrefix(host, "127.") || strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "100.") {
		return "That page is on your own network, so I leave it alone."
	}
	full := host + path
	for _, b := range sightBlockHosts {
		if strings.Contains(full, b) {
			return "That looks like a private page (money, mail, health or a login), so I won't look at it."
		}
	}
	for _, b := range strings.Split(sightExtraB, ",") {
		if b = strings.ToLower(strings.TrimSpace(b)); b != "" && strings.Contains(host, b) {
			return "You told me not to look at that site."
		}
	}
	for _, p := range sightBlockPaths {
		if strings.Contains(path, p) {
			return "That looks like a login or payment page, so I won't look at it."
		}
	}
	return ""
}

func sightClean(s string, n int) string {
	s = digitRun.ReplaceAllString(redact(s), "[number removed]")
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

var steamAppURL = regexp.MustCompile(`^/app/(\d{1,9})`)

// GET /api/sight/ping: the extension checks its token and shows who it is paired as.
func (a *App) handleSightPing(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if !(c.Kind == callerPhone || c.Kind == callerMain) || c.BadToken {
		writeErr(w, http.StatusUnauthorized, "Pair this extension first (a 6-digit code from Lucille).")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "label": c.Label, "version": version})
}

// POST /api/sight {"url","title","selection","text","question","local"}
func (a *App) handleSight(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	if !(c.Kind == callerPhone || c.Kind == callerMain) || c.BadToken {
		writeErr(w, http.StatusUnauthorized, "Pair this extension first (a 6-digit code from Lucille).")
		return
	}
	var req struct {
		URL, Title, Selection, Text, Question, Query string
		Local, HasPassword                           bool
		Items                                        []struct {
			I                   int
			Title, URL, Snippet string
		}
	}
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.URL) == "" {
		writeErr(w, http.StatusBadRequest, `Send {"url": "...", "title": "...", "text": "...", "question": "..."}.`)
		return
	}
	u, err := url.Parse(req.URL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "That is not a web address.")
		return
	}
	if why := sightBlocked(u); why != "" {
		writeJSON(w, map[string]any{"ok": true, "refused": true, "text": why})
		return
	}
	if req.HasPassword {
		writeJSON(w, map[string]any{"ok": true, "refused": true, "text": "There's a password box on that page, so I won't look at it."})
		return
	}
	page := "PAGE TITLE: " + sightClean(req.Title, 200) + "\nADDRESS: " + u.Scheme + "://" + u.Host + u.Path + "\n"
	if sel := sightClean(req.Selection, sightMaxSel); sel != "" {
		page += "\nTHE USER HAS SELECTED THIS TEXT (this is probably what they mean):\n" + sel + "\n"
	}
	if t := sightClean(req.Text, sightMaxText); t != "" {
		page += "\nVISIBLE PAGE TEXT:\n" + t + "\n"
	}

	var card *GameCard
	if strings.HasSuffix(u.Hostname(), "store.steampowered.com") {
		if m := steamAppURL.FindStringSubmatch(u.Path); m != nil {
			id, _ := strconv.Atoi(m[1])
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			v, _ := a.cache.get(fmt.Sprintf("game:%d:", id), 2*time.Hour, func() (any, error) { return a.buildCard(ctx, id, ""), nil })
			cancel()
			cp := *(v.(*GameCard))
			cp.Mark = a.readMarks()[cp.Key]
			card = &cp
			page += fmt.Sprintf("\nSTEAM DATA I LOOKED UP: %s. Steam reviews: %s (%d%% of %d). Metacritic: %d. Price on Steam: %s. Lowest price ever: %s.",
				cp.Name, cp.ReviewWord, cp.ReviewPct, cp.ReviewTotal, cp.Metacritic, cp.Price, cp.LowestEver)
			if cp.Mark != nil {
				page += fmt.Sprintf(" The user has marked it: like=%q, wish list=%v.", cp.Mark.Like, cp.Mark.Wish)
			}
			page += "\n"
		}
	}

	q := sightClean(req.Question, sightMaxQ)
	search := len(req.Items) > 0
	var kept []sightItem
	if search {
		page += "\nTHE USER SEARCHED FOR: " + sightClean(req.Query, 200) + "\nTHE RESULTS ON THE PAGE, numbered:\n"
		for _, it := range req.Items {
			if len(kept) >= 20 {
				break
			}
			si := sightItem{I: it.I, Title: sightClean(it.Title, 160), URL: sightClean(it.URL, 160), Snippet: sightClean(it.Snippet, 260)}
			if si.Title == "" {
				continue
			}
			kept = append(kept, si)
			page += fmt.Sprintf("[%d] %s (%s) %s\n", si.I, si.Title, si.URL, si.Snippet)
		}
	}
	if q == "" {
		switch {
		case search:
			q = "Which of these results is really what I'm looking for?"
		case strings.TrimSpace(req.Selection) != "":
			q = "Explain the text I selected."
		default:
			q = "What should I know about this page, and is there anything you can help me with on it?"
		}
	}
	note := "The user is browsing the web and pressed the button that lets you see their current page. You can read only what is shown above; you cannot click, scroll or type for them. Help where you can: say what the page is, answer their question, explain an error or a confusing step, summarize a long page, point out anything that looks like a scam or a bad deal, and for games use the Steam data. Be accurate and brief. If the page text does not contain the answer, say so instead of guessing. Never ask them for passwords, card numbers or personal details."
	format := `Reply with ONLY one JSON object and nothing else: {"summary": "two to four plain sentences, in your own voice, saying what this page shows and what matters for what they asked", "highlights": [{"quote": "four to twenty words copied EXACTLY, character for character, from the page text above", "why": "under 14 words"}]}. Give up to 6 highlights: the passages that answer what they are looking for. A quote that is not word-for-word on the page is thrown away.`
	if search {
		format = `Reply with ONLY one JSON object and nothing else: {"summary": "two to four plain sentences, in your own voice, saying what these results show and which you would open first and why", "picks": [{"i": the result's number, "verdict": "best" or "good" or "skip", "why": "under 14 words"}]}. Mark at most 3 as best (what they are really looking for), others that are worth a look as good, and mark skip for ads, spam, content farms, shops when they want information, or anything off-topic. Use only numbers from the list.`
	}
	ctx, cancel := context.WithTimeout(r.Context(), 100*time.Second)
	defer cancel()
	text, model, provider, err := a.generateChatReply(ctx, ChatRequest{Message: page + "\nTHE USER ASKS: " + q + "\n\n" + format, system: a.systemPrompt() + "\n\n" + note, maxTokens: 900, noCloud: req.Local})
	if err != nil || strings.TrimSpace(text) == "" {
		writeJSON(w, map[string]any{"ok": false, "error": "I couldn't think about that page just now. Try again in a minute."})
		return
	}
	out := map[string]any{"ok": true, "model": model, "provider": provider, "kept_on_pc": provider == "local-swarm", "note": "Nothing from this page was saved."}
	summary, hl, picks := parseSight(stripThink(text), sightClean(req.Text+"\n"+req.Selection, 2*sightMaxText), kept)
	out["text"], out["highlights"], out["picks"] = summary, hl, picks
	if card != nil {
		out["game"] = card
	}
	writeJSON(w, out)
}

type sightItem struct {
	I                   int
	Title, URL, Snippet string
}

type sightHL struct {
	Quote string `json:"quote"`
	Why   string `json:"why"`
}

type sightPick struct {
	I       int    `json:"i"`
	Verdict string `json:"verdict"`
	Why     string `json:"why"`
}

var wsRuns = regexp.MustCompile(`\s+`)

func normWS(s string) string {
	return strings.ToLower(strings.TrimSpace(wsRuns.ReplaceAllString(s, " ")))
}

// parseSight reads the model's JSON, tolerating chatter around it, and keeps only what can be checked: a highlight
// must be word-for-word on the page, a pick must name a result that was really sent. If the model did not return JSON
// at all, its plain words become the summary and nothing is highlighted.
func parseSight(raw, pageText string, items []sightItem) (summary string, hl []sightHL, picks []sightPick) {
	i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if i < 0 || j <= i {
		return clip(raw, 900), nil, nil
	}
	var m struct {
		Summary    string
		Highlights []sightHL
		Picks      []sightPick
	}
	if err := json.Unmarshal([]byte(raw[i:j+1]), &m); err != nil {
		return clip(strings.TrimSpace(raw), 900), nil, nil
	}
	summary = clip(m.Summary, 900)
	page := normWS(pageText)
	for _, h := range m.Highlights {
		q := strings.TrimSpace(wsRuns.ReplaceAllString(h.Quote, " "))
		if len(q) < 12 || len(q) > 260 || !strings.Contains(page, normWS(q)) || len(hl) >= 6 {
			continue // not on the page, so it is not shown
		}
		hl = append(hl, sightHL{Quote: q, Why: clip(h.Why, 120)})
	}
	valid := map[int]bool{}
	for _, it := range items {
		valid[it.I] = true
	}
	best := 0
	for _, p := range m.Picks {
		if !valid[p.I] || (p.Verdict != "best" && p.Verdict != "good" && p.Verdict != "skip") {
			continue
		}
		if p.Verdict == "best" {
			if best++; best > 3 {
				p.Verdict = "good"
			}
		}
		picks = append(picks, sightPick{I: p.I, Verdict: p.Verdict, Why: clip(p.Why, 120)})
	}
	return summary, hl, picks
}
