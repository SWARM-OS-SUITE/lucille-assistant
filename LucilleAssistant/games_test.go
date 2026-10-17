package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestGameMarksLikeWishAndDigest(t *testing.T) {
	a := threadsApp(t)
	post := func(body map[string]any) map[string]any {
		w := postJSON(a.handleGameMark, "/api/games/mark", body, nil)
		if w.Code != 200 {
			t.Fatalf("mark %v: %d %s", body, w.Code, w.Body)
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	post(map[string]any{"appid": 1145360, "name": "Hades", "wish": true})
	post(map[string]any{"appid": 1245620, "name": "Elden Ring", "like": "dislike"})
	post(map[string]any{"appid": 1145360, "name": "Hades", "like": "like"}) // wish stays
	marks := a.readMarks()
	h := marks["1145360"]
	if h == nil || !h.Wish || h.Like != "like" {
		t.Fatalf("a like must not clear the wish: %+v", h)
	}
	d := a.gamesDigest()
	for _, want := range []string{"wish list: Hades", "liked: Hades", "disliked: Elden Ring"} {
		if !strings.Contains(d, want) {
			t.Errorf("digest %q missing %q", d, want)
		}
	}
	post(map[string]any{"appid": 1245620, "name": "Elden Ring", "like": ""}) // un-dislike: nothing left to keep
	if _, ok := a.readMarks()["1245620"]; ok {
		t.Fatal("a game with no wish and no like should be forgotten")
	}
}

func TestGameMarkRules(t *testing.T) {
	a := threadsApp(t)
	agent := Caller{Kind: callerAgent}
	if w := postJSON(a.handleGameMark, "/x", map[string]any{"name": "X", "wish": true}, &agent); w.Code != 403 {
		t.Fatalf("Dr. Boltz's token must not edit the user's game list: %d", w.Code)
	}
	if w := postJSON(a.handleGameMark, "/x", map[string]any{"name": "X", "like": "love"}, nil); w.Code != 400 {
		t.Fatalf("bad like value accepted: %d", w.Code)
	}
	if w := postJSON(a.handleGameMark, "/x", map[string]any{"name": "", "wish": true}, nil); w.Code != 400 {
		t.Fatalf("empty name accepted: %d", w.Code)
	}
}

func TestPlainHTMLAndKeys(t *testing.T) {
	if got := plainHTML("Defy the <b>god</b> of the dead &amp; escape"); got != "Defy the god of the dead & escape" {
		t.Fatalf("got %q", got)
	}
	if gameKey(0, "Hades II!") != "t:hades-ii" || gameKey(5, "x") != "5" {
		t.Fatal("game keys")
	}
}

func TestSteamImageOnlyFromSteamCDN(t *testing.T) {
	if !steamImage("https://shared.akamai.steamstatic.com/store_item_assets/steam/apps/1/ss_1.1920x1080.jpg") {
		t.Fatal("Steam's CDN must be accepted")
	}
	for _, bad := range []string{"http://shared.akamai.steamstatic.com/x.jpg", "https://evilsteamstatic.com/x.jpg", "https://example.com/steamstatic.com/x.jpg", "javascript:alert(1)", ""} {
		if steamImage(bad) {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestSightBlocksPrivatePages(t *testing.T) {
	for _, bad := range []string{
		"https://www.chase.com/personal", "https://secure.bankofamerica.com/x", "https://mail.google.com/mail/u/0", "https://www.paypal.com/myaccount",
		"https://mychart.example.org/", "https://store.example.com/checkout/step2", "https://accounts.google.com/signin", "https://example.com/login?next=/",
		"file:///etc/passwd", "http://127.0.0.1:8403/", "http://192.168.1.1/", "https://login.live.com/", "https://vault.bitwarden.com/",
	} {
		u, _ := url.Parse(bad)
		if sightBlocked(u) == "" {
			t.Errorf("%s must be refused", bad)
		}
	}
	for _, ok := range []string{"https://store.steampowered.com/app/1145360/Hades/", "https://en.wikipedia.org/wiki/Hades_(video_game)", "https://www.reddit.com/r/linux/", "https://github.com/x/y"} {
		u, _ := url.Parse(ok)
		if why := sightBlocked(u); why != "" {
			t.Errorf("%s was refused: %s", ok, why)
		}
	}
}

func TestSightCleansNumbersAndSecrets(t *testing.T) {
	got := sightClean("card 4111 1111 1111 1111 ssn 123-45-6789 key sk-abcdefghijklmnopqrstuv1234 and the year 2026", 500)
	for _, leak := range []string{"4111", "123-45-6789", "sk-abcdefghijkl"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q leaked in %q", leak, got)
		}
	}
	if !strings.Contains(got, "2026") {
		t.Errorf("an ordinary year must survive: %q", got)
	}
}

func TestSightNeedsAPairedToken(t *testing.T) {
	a := threadsApp(t)
	for _, c := range []*Caller{nil, {Kind: callerPC}, {Kind: callerOpen}, {Kind: callerPhone, BadToken: true}} {
		if w := postJSON(a.handleSight, "/api/sight", map[string]string{"url": "https://example.com/", "text": "hi"}, c); w.Code != 401 {
			t.Fatalf("caller %+v must be refused, got %d", c, w.Code)
		}
	}
	w := postJSON(a.handleSight, "/api/sight", map[string]string{"url": "https://www.chase.com/", "text": "balance"}, &Caller{Kind: callerPhone, Label: "Chrome"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"refused":true`) {
		t.Fatalf("a bank page must come back refused: %d %s", w.Code, w.Body)
	}
}

func TestParseSightKeepsOnlyWhatIsReal(t *testing.T) {
	page := "Hades is a roguelike.\n  The game costs  $24.99 on Steam and often goes on sale for less."
	raw := "Sure! {\"summary\":\"It is a roguelike.\",\"highlights\":[" +
		"{\"quote\":\"costs $24.99 on Steam\",\"why\":\"the price\"}," +
		"{\"quote\":\"it won three awards and sells for $5\",\"why\":\"made up\"}]," +
		"\"picks\":[{\"i\":2,\"verdict\":\"best\",\"why\":\"official\"},{\"i\":99,\"verdict\":\"best\",\"why\":\"not in the list\"},{\"i\":3,\"verdict\":\"great\",\"why\":\"bad verdict\"}]} thanks"
	sum, hl, picks := parseSight(raw, page, []sightItem{{I: 1}, {I: 2}, {I: 3}})
	if sum != "It is a roguelike." {
		t.Errorf("summary %q", sum)
	}
	if len(hl) != 1 || hl[0].Quote != "costs $24.99 on Steam" {
		t.Errorf("only the real quote may survive: %+v", hl)
	}
	if len(picks) != 1 || picks[0].I != 2 {
		t.Errorf("only valid picks may survive: %+v", picks)
	}
	if s, h, p := parseSight("I think it's a game page.", page, nil); s == "" || h != nil || p != nil {
		t.Errorf("plain words become the summary: %q %v %v", s, h, p)
	}
}

func TestLauncherGatesAndKillSwitch(t *testing.T) {
	if launchTool("claude") != "claude" || launchTool("antigravity") != "agy" || launchTool("chatgpt") != "" || launchTool("dev-claude") != "" {
		t.Fatal("only Claude and Antigravity can be started; ChatGPT cannot")
	}
	t.Setenv("LUCILLE_LAUNCH", "0")
	if launchEnabled() {
		t.Fatal("LUCILLE_LAUNCH=0 must switch launching off")
	}
	a := threadsApp(t)
	// with launching off, "Approve and start it now" is refused and nothing is queued or started
	w := postJSON(a.handleJobCreate, "/api/jobs", map[string]any{"anchor": "item:SW-01", "to": "claude", "prompt": "do it", "launch": true}, nil)
	if w.Code != 409 {
		t.Fatalf("launch while switched off must be refused: %d %s", w.Code, w.Body)
	}
	if len(a.readJobs()) != 0 {
		t.Fatal("a refused launch must not leave a job behind")
	}
	// a normal approval (no launch) still goes to the mailbox and starts nothing
	w = postJSON(a.handleJobCreate, "/api/jobs", map[string]any{"anchor": "item:SW-01", "to": "claude", "prompt": "do it"}, nil)
	if w.Code != 200 || len(a.readJobs()) != 1 || a.readJobs()[0].Status != "queued" {
		t.Fatalf("mailbox approval: %d %s", w.Code, w.Body)
	}
	if stopRunning("jnothing") {
		t.Fatal("stopping a job that is not running must report false")
	}
}
