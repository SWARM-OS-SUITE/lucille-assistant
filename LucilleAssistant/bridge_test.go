package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── harness ─────────────────────────────────────────────────────

type bridgeHarness struct {
	t      *testing.T
	app    *App
	h      http.Handler
	dir    string
	agent  string // paired agent token
	phone  string // paired phone token
	mainT  string
	mu     sync.Mutex
	clock  time.Time
	llmOut func(system, user string) (string, error)
}

func newBridgeHarness(t *testing.T) *bridgeHarness {
	t.Helper()
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.DataDir = dir
	cfg.Token = "main-token-for-tests"
	cfg.Bridge = normalizeBridge(BridgeConfig{MaxBudgetUSD: 2, MaxMinutes: 20, Repos: []BridgeRepo{
		{Name: "lucille", Path: dir, WorkDir: filepath.Join(dir, "w1"), Desc: "the Go service"},
		{Name: "boltz", Path: dir, WorkDir: filepath.Join(dir, "w2"), Scope: "30-source/x", Desc: "the Python brain"},
	}})
	cfg.Actions = normalizeActions([]Action{
		{ID: "say-hi", Label: "Say hi", Command: []string{"/usr/bin/echo", "hi"}, AgentAuto: true},
		{ID: "ask-first", Label: "Ask first", Command: []string{"/usr/bin/echo", "asked"}, Sudo: false},
		{ID: "nuke", Label: "Nuke", Command: []string{"/usr/bin/true"}, Destructive: true},
	})
	app := &App{cfg: cfg, store: st, cache: newCache(), pairing: newPairManager(st)}
	b, err := newBridge(dir, cfg.Bridge)
	if err != nil {
		t.Fatal(err)
	}
	app.bridge = b
	hn := &bridgeHarness{t: t, app: app, dir: dir, mainT: cfg.Token, clock: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	b.now = func() time.Time { return hn.clock } // read only while b.mu is held, or before goroutines start
	app.llm = func(ctx context.Context, system, user string, n int) (string, error) {
		if hn.llmOut == nil {
			return "", errors.New("no model in this test")
		}
		return hn.llmOut(system, user)
	}
	_, hn.agent, _ = st.pairDeviceRole("boltz-worker", roleAgent)
	_, hn.phone, _ = st.pairDeviceRole("My Phone", rolePhone)
	hn.h = auth(cfg.Token, st, app.newMux())
	return hn
}

func (hn *bridgeHarness) advance(d time.Duration) {
	hn.app.bridge.mu.Lock()
	hn.clock = hn.clock.Add(d)
	hn.app.bridge.mu.Unlock()
}

// req sends one request as: pc (loopback, no token), agent (loopback + agent token),
// stale (loopback + a token that matches nothing), phone/main (remote with that token),
// open (remote, no token).
func (hn *bridgeHarness) req(method, path, who string, body any, hdr ...string) (int, map[string]any) {
	hn.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rd)
	r.Host = "127.0.0.1:8403"
	r.RemoteAddr = "127.0.0.1:50000"
	switch who {
	case "agent":
		r.Header.Set("X-Swarm-Auth", hn.agent)
	case "stale":
		r.Header.Set("X-Swarm-Auth", "not-a-real-token")
	case "phone":
		r.RemoteAddr = "100.82.1.5:50000"
		r.Header.Set("X-Swarm-Auth", hn.phone)
	case "main":
		r.RemoteAddr = "100.82.1.5:50000"
		r.Header.Set("X-Swarm-Auth", hn.mainT)
	case "open":
		r.RemoteAddr = "100.82.1.5:50000"
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rr := httptest.NewRecorder()
	hn.h.ServeHTTP(rr, r)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr.Code, out
}

func (hn *bridgeHarness) mustOK(code int, out map[string]any, want int) map[string]any {
	hn.t.Helper()
	if code != want {
		hn.t.Fatalf("status %d, want %d: %v", code, want, out)
	}
	return out
}

func ch(m map[string]any) map[string]any { return m["challenge"].(map[string]any) }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (hn *bridgeHarness) texts(kind string) []string {
	var out []string
	for _, ev := range hn.app.bridge.eventsSince(0, 0) {
		if kind == "" || ev.Kind == kind {
			out = append(out, ev.Who+": "+ev.Text)
		}
	}
	return out
}

func newChallenge(hn *bridgeHarness, title string) string {
	hn.t.Helper()
	code, out := hn.req("POST", "/api/bridge/challenges", "pc", map[string]string{"title": title, "body": "do it", "repo": "lucille"})
	hn.mustOK(code, out, 200)
	return ch(out)["id"].(string)
}

// ── who is calling ──────────────────────────────────────────────

func TestAuthIdentifiesCallers(t *testing.T) {
	st, _ := openStore(t.TempDir())
	_, agentTok, _ := st.pairDeviceRole("worker", roleAgent)
	_, phoneTok, _ := st.pairDeviceRole("phone", rolePhone)
	var got Caller
	h := auth("main-token", st, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = callerOf(r)
		w.WriteHeader(200)
	}))
	cases := []struct {
		name   string
		remote string
		token  string
		kind   callerKind
		bad    bool
		status int
	}{
		{"loopback no token is the PC", "127.0.0.1:1", "", callerPC, false, 200},
		{"agent token stays the agent on loopback", "127.0.0.1:1", agentTok, callerAgent, false, 200},
		{"phone token on loopback is a phone", "127.0.0.1:1", phoneTok, callerPhone, false, 200},
		{"stale token on loopback is flagged", "127.0.0.1:1", "revoked", callerPC, true, 200},
		{"phone over the tailnet", "100.82.1.5:1", phoneTok, callerPhone, false, 200},
		{"agent over the tailnet", "100.82.1.5:1", agentTok, callerAgent, false, 200},
		{"main token", "100.82.1.5:1", "main-token", callerMain, false, 200},
		{"remote without a token is refused", "100.82.1.5:1", "", "", false, 401},
		{"remote with a wrong token is refused", "100.82.1.5:1", "nope", "", false, 401},
	}
	for _, c := range cases {
		got = Caller{}
		r := httptest.NewRequest("GET", "/api/anything", nil)
		r.RemoteAddr = c.remote
		if c.token != "" {
			r.Header.Set("X-Swarm-Auth", c.token)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, r)
		if rr.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.name, rr.Code, c.status)
			continue
		}
		if c.status == 200 && (got.Kind != c.kind || got.BadToken != c.bad) {
			t.Errorf("%s: caller %+v, want kind %s bad=%v", c.name, got, c.kind, c.bad)
		}
	}
}

func TestPairRoles(t *testing.T) {
	hn := newBridgeHarness(t)
	// minted on the PC, role fixed by the PC
	code, out := hn.req("GET", "/api/pair/new?role=agent", "pc", nil)
	hn.mustOK(code, out, 200)
	if out["role"] != "agent" {
		t.Fatalf("role not echoed: %v", out)
	}
	// redeemed from anywhere with no token
	c2, o2 := hn.req("POST", "/api/pair", "open", map[string]string{"code": out["code"].(string), "label": "worker\x1b[31m"})
	hn.mustOK(c2, o2, 200)
	d, ok := hn.app.store.DeviceForToken(o2["token"].(string))
	if !ok || d.Role != roleAgent {
		t.Fatalf("device = %+v ok=%v, want an agent", d, ok)
	}
	if strings.ContainsRune(d.Label, 0x1b) {
		t.Errorf("label kept an escape character: %q", d.Label)
	}
	// a phone code stays a phone, stored without a role for compatibility
	c3, o3 := hn.req("GET", "/api/pair/new", "pc", nil)
	hn.mustOK(c3, o3, 200)
	_, o4 := hn.req("POST", "/api/pair", "open", map[string]string{"code": o3["code"].(string), "label": "p"})
	if d, _ := hn.app.store.DeviceForToken(o4["token"].(string)); d.Role != "" {
		t.Errorf("phone stored role %q, want empty", d.Role)
	}
	// a token can never mint access: not the agent's, and not a revoked one
	for _, who := range []string{"agent", "stale"} {
		if c, _ := hn.req("GET", "/api/pair/new?role=phone", who, nil); c != 403 {
			t.Errorf("%s token minting a pairing code: %d, want 403", who, c)
		}
	}
	// bad role, and minting from off the PC
	if c, _ := hn.req("GET", "/api/pair/new?role=root", "pc", nil); c != 400 {
		t.Errorf("bad role: %d, want 400", c)
	}
	if c, _ := hn.req("GET", "/api/pair/new?role=agent", "main", nil); c != 403 {
		t.Errorf("minting from off the PC: %d, want 403", c)
	}
	// the code is single use even for an agent
	if c, _ := hn.req("POST", "/api/pair", "open", map[string]string{"code": out["code"].(string)}); c != 401 {
		t.Errorf("reused agent code: %d, want 401", c)
	}
}

// ── cross-site guard ────────────────────────────────────────────

func TestBrowserSafe(t *testing.T) {
	hn := newBridgeHarness(t)
	body := map[string]string{"title": "t", "repo": "lucille"}
	cases := []struct {
		name string
		hdr  []string
		host string
		want int
	}{
		{"no Origin (phone app, curl, CLI)", nil, "", 200},
		{"same origin", []string{"Origin", "http://127.0.0.1:8403", "Sec-Fetch-Site", "same-origin"}, "", 200},
		{"web page on another origin", []string{"Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site"}, "", 403},
		{"cross-site without an Origin header", []string{"Sec-Fetch-Site", "cross-site"}, "", 403},
		{"another local port counts as cross-site", []string{"Origin", "http://localhost:3000", "Sec-Fetch-Site", "same-site"}, "", 403},
		{"sandboxed iframe (Origin null)", []string{"Origin", "null"}, "", 403},
		{"DNS rebinding: Host is an attacker's name", nil, "attacker.example.com:8403", 403},
		{"tailnet short name", nil, "debian:8403", 200},
	}
	for i, c := range cases {
		r := httptest.NewRequest("POST", "/api/bridge/challenges", bytes.NewReader(mustJSON(map[string]string{"title": "guard test " + string(rune('a'+i)), "repo": body["repo"]})))
		r.RemoteAddr = "127.0.0.1:50000"
		r.Host = "127.0.0.1:8403"
		if c.host != "" {
			r.Host = c.host
		}
		for j := 0; j+1 < len(c.hdr); j += 2 {
			r.Header.Set(c.hdr[j], c.hdr[j+1])
		}
		rr := httptest.NewRecorder()
		hn.h.ServeHTTP(rr, r)
		if rr.Code != c.want {
			t.Errorf("%s: %d, want %d (%s)", c.name, rr.Code, c.want, rr.Body.String())
		}
	}
	// reads of the powerful routes are guarded too
	for _, p := range []string{"/api/bridge", "/api/actions", "/api/pair/new"} {
		code, _ := hn.req("GET", p, "pc", nil, "Origin", "https://evil.example")
		if code != 403 {
			t.Errorf("GET %s from a foreign origin: %d, want 403", p, code)
		}
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// Every route that can reach the coding agent, root, or pairing must sit behind
// browserSafe. Reading main.go keeps this honest when someone adds a route later.
func TestPowerfulRoutesAreGuarded(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	route := regexp.MustCompile(`mux\.HandleFunc\("[A-Z]+ (/api/(?:bridge|actions|pair/new)[^"]*)"(.*)`)
	n := 0
	for _, line := range strings.Split(string(src), "\n") {
		if m := route.FindStringSubmatch(line); m != nil {
			n++
			if !strings.Contains(m[2], "browserSafe(") {
				t.Errorf("route %s is registered without browserSafe", m[1])
			}
		}
	}
	if n < 15 {
		t.Errorf("found only %d guarded routes; did the route table move?", n)
	}
}

// ── permissions ─────────────────────────────────────────────────

func TestRolesAreEnforced(t *testing.T) {
	hn := newBridgeHarness(t)
	id := newChallenge(hn, "first")

	humanOnly := []struct{ method, path string }{
		{"POST", "/api/bridge/challenges"},
		{"POST", "/api/bridge/generate"},
		{"POST", "/api/bridge/challenges/" + id + "/decision"},
		{"GET", "/api/bridge/approvals"},
		{"POST", "/api/bridge/approvals/x/decision"},
	}
	for _, e := range humanOnly {
		if c, _ := hn.req(e.method, e.path, "agent", map[string]any{"title": "x", "repo": "lucille", "action": "approve", "approve": true}); c != 403 {
			t.Errorf("agent %s %s: %d, want 403", e.method, e.path, c)
		}
		if c, _ := hn.req(e.method, e.path, "stale", map[string]any{"title": "x", "repo": "lucille"}); c != 401 {
			t.Errorf("stale-token %s %s: %d, want 401", e.method, e.path, c)
		}
	}
	agentOnly := []struct{ method, path string }{
		{"GET", "/api/bridge/inbox"},
		{"POST", "/api/bridge/challenges/" + id + "/claim"},
		{"POST", "/api/bridge/challenges/" + id + "/log"},
		{"POST", "/api/bridge/challenges/" + id + "/complete"},
		{"POST", "/api/bridge/actions/say-hi/request"},
	}
	for _, e := range agentOnly {
		for _, who := range []string{"pc", "phone", "main"} {
			if c, _ := hn.req(e.method, e.path, who, map[string]any{}); c != 403 {
				t.Errorf("%s %s %s: %d, want 403", who, e.method, e.path, c)
			}
		}
	}
	// the agent may read the state and talk
	if c, _ := hn.req("GET", "/api/bridge", "agent", nil); c != 200 {
		t.Errorf("agent reading state: %d", c)
	}
	if c, _ := hn.req("POST", "/api/bridge/say", "agent", map[string]string{"text": "hi", "to": "boltz"}); c != 200 {
		t.Errorf("agent saying something: %d", c)
	}
	// no anonymous access, even on a server that has no main token
	hn.app.cfg.Token = ""
	hn.h = auth("", hn.app.store, hn.app.newMux())
	if c, _ := hn.req("GET", "/api/bridge", "open", nil); c != 401 {
		t.Errorf("anonymous remote reading the bridge: %d, want 401", c)
	}
}

// ── lifecycle ───────────────────────────────────────────────────

func TestChallengeLifecycle(t *testing.T) {
	hn := newBridgeHarness(t)
	id := newChallenge(hn, "Add a thing")

	// the inbox hands the agent the oldest open challenge and the repo policy
	c, in := hn.req("GET", "/api/bridge/inbox", "agent", nil)
	hn.mustOK(c, in, 200)
	if in["challenge"].(map[string]any)["id"] != id {
		t.Fatalf("inbox challenge = %v, want %s", in["challenge"], id)
	}
	if eng := in["engine"].(map[string]any); eng["max_budget_usd"].(float64) != 2 || eng["max_minutes"].(float64) != 20 {
		t.Errorf("engine limits = %v", eng)
	}
	if len(in["repos"].([]any)) != 2 {
		t.Errorf("repos = %v", in["repos"])
	}
	// the agent is told which actions it may ask for: none of them destructive
	ids := map[string]bool{}
	for _, a := range in["actions"].([]any) {
		ids[a.(map[string]any)["id"].(string)] = true
	}
	if !ids["say-hi"] || !ids["ask-first"] || ids["nuke"] {
		t.Errorf("inbox actions = %v; want say-hi and ask-first but never nuke", ids)
	}

	// claim, then nobody else can claim it
	c, o := hn.req("POST", "/api/bridge/challenges/"+id+"/claim", "agent", nil)
	if hn.mustOK(c, o, 200); ch(o)["status"] != "working" || ch(o)["branch"] != "boltz/"+id {
		t.Fatalf("after claim: %v", ch(o))
	}
	if c, _ := hn.req("POST", "/api/bridge/challenges/"+id+"/claim", "agent", nil); c != 409 {
		t.Errorf("second claim: %d, want 409", c)
	}
	_, in = hn.req("GET", "/api/bridge/inbox", "agent", nil)
	if in["challenge"] != nil {
		t.Errorf("inbox still offers work while one is running: %v", in["challenge"])
	}

	// progress shows up in the shared transcript, and is scrubbed
	c, o = hn.req("POST", "/api/bridge/challenges/"+id+"/log", "agent", map[string]any{"lines": []string{"editing main.go", "\x1b]0;pwned\x07ran tests\x1b[2J"}})
	hn.mustOK(c, o, 200)
	for _, tx := range hn.texts("term") {
		if strings.ContainsAny(tx, "\x1b\x07") {
			t.Errorf("escape characters reached the transcript: %q", tx)
		}
	}

	// the agent cannot approve its own work
	commits := []map[string]string{{"repo": "lucille", "branch": "boltz/" + id, "sha": "abc1234def", "subject": "Add a thing"}}
	c, o = hn.req("POST", "/api/bridge/challenges/"+id+"/complete", "agent", map[string]any{"status": "review", "summary": "done", "commits": commits})
	hn.mustOK(c, o, 200)
	if c, _ := hn.req("POST", "/api/bridge/challenges/"+id+"/complete", "agent", map[string]any{"status": "merged", "commits": commits}); c != 409 {
		t.Errorf("merging before approval: %d, want 409", c)
	}
	if c, _ := hn.req("POST", "/api/bridge/challenges/"+id+"/decision", "agent", map[string]string{"action": "approve"}); c != 403 {
		t.Errorf("agent approving itself: %d, want 403", c)
	}
	_, in = hn.req("GET", "/api/bridge/inbox", "agent", nil)
	if len(in["merges"].([]any)) != 0 {
		t.Errorf("a merge was offered before approval: %v", in["merges"])
	}

	// a phone approves; only then does the agent see it as mergeable
	c, o = hn.req("POST", "/api/bridge/challenges/"+id+"/decision", "phone", map[string]string{"action": "approve"})
	if hn.mustOK(c, o, 200); ch(o)["status"] != "approved" || ch(o)["decided_by"] != "My Phone" {
		t.Fatalf("after approval: %v", ch(o))
	}
	_, in = hn.req("GET", "/api/bridge/inbox", "agent", nil)
	if m := in["merges"].([]any); len(m) != 1 || m[0].(map[string]any)["id"] != id {
		t.Fatalf("merges = %v", in["merges"])
	}
	c, o = hn.req("POST", "/api/bridge/challenges/"+id+"/complete", "agent", map[string]any{"status": "merged", "summary": "merged ff"})
	if hn.mustOK(c, o, 200); ch(o)["status"] != "merged" {
		t.Fatalf("after merge: %v", ch(o))
	}
	// the state survives a restart
	b2, err := newBridge(hn.dir, hn.app.cfg.Bridge)
	if err != nil {
		t.Fatal(err)
	}
	if len(b2.chal) != 1 || b2.chal[0].Status != stMerged {
		t.Errorf("reloaded challenges = %+v", b2.chal)
	}
}

func TestCompleteValidation(t *testing.T) {
	hn := newBridgeHarness(t)
	id := newChallenge(hn, "validate me")
	hn.req("POST", "/api/bridge/challenges/"+id+"/claim", "agent", nil)
	good := map[string]string{"repo": "lucille", "branch": "boltz/" + id, "sha": "abc1234", "subject": "x"}
	mut := func(k, v string) map[string]string {
		m := map[string]string{}
		for kk, vv := range good {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	bad := []struct {
		name    string
		commits []map[string]string
	}{
		{"unknown repo", []map[string]string{mut("repo", "etc")}},
		{"branch outside boltz/", []map[string]string{mut("branch", "main")}},
		{"branch with traversal", []map[string]string{mut("branch", "boltz/c0001/../../x")}},
		{"non-hex sha", []map[string]string{mut("sha", "not-a-sha; rm -rf /")}},
		{"no commits for review", nil},
	}
	for _, b := range bad {
		c, o := hn.req("POST", "/api/bridge/challenges/"+id+"/complete", "agent", map[string]any{"status": "review", "commits": b.commits})
		if c != 400 {
			t.Errorf("%s: %d, want 400 (%v)", b.name, c, o)
		}
	}
	if c, _ := hn.req("POST", "/api/bridge/challenges/"+id+"/complete", "agent", map[string]any{"status": "approved"}); c != 409 {
		t.Errorf("agent setting approved: %d, want 409", c)
	}
	// failing is always allowed from working, and a person can retry it
	if c, o := hn.req("POST", "/api/bridge/challenges/"+id+"/complete", "agent", map[string]any{"status": "failed", "summary": "tests did not pass"}); c != 200 {
		t.Fatalf("failing: %d %v", c, o)
	}
	c, o := hn.req("POST", "/api/bridge/challenges/"+id+"/decision", "pc", map[string]string{"action": "retry"})
	if hn.mustOK(c, o, 200); ch(o)["status"] != "open" || ch(o)["branch"] != nil {
		t.Errorf("after retry: %v", ch(o))
	}
}

func TestCancelAndStaleClaims(t *testing.T) {
	hn := newBridgeHarness(t)
	a, b := newChallenge(hn, "one"), newChallenge(hn, "two")
	hn.req("POST", "/api/bridge/challenges/"+a+"/claim", "agent", nil)
	if c, _ := hn.req("POST", "/api/bridge/challenges/"+b+"/claim", "agent", nil); c != 409 {
		t.Fatalf("second concurrent claim: %d, want 409", c)
	}
	// cancelling tells the worker at its next report
	hn.req("POST", "/api/bridge/challenges/"+a+"/decision", "pc", map[string]string{"action": "cancel"})
	_, o := hn.req("POST", "/api/bridge/challenges/"+a+"/log", "agent", map[string]any{"lines": []string{"still going"}})
	if o["cancelled"] != true {
		t.Errorf("log after cancel = %v, want cancelled", o)
	}
	if c, _ := hn.req("POST", "/api/bridge/challenges/"+a+"/decision", "pc", map[string]string{"action": "approve"}); c != 409 {
		t.Errorf("approving a cancelled challenge: %d, want 409", c)
	}
	// a worker that dies mid-claim does not block the queue forever
	hn.req("POST", "/api/bridge/challenges/"+b+"/claim", "agent", nil)
	c2 := newChallenge(hn, "three")
	hn.advance(claimStale + time.Minute)
	if c, o := hn.req("POST", "/api/bridge/challenges/"+c2+"/claim", "agent", nil); c != 200 {
		t.Fatalf("claim after the old claim went stale: %d %v", c, o)
	}
	code, st := hn.req("GET", "/api/bridge", "pc", nil)
	hn.mustOK(code, st, 200)
	for _, c := range st["challenges"].([]any) {
		if m := c.(map[string]any); m["id"] == b && m["status"] != "failed" {
			t.Errorf("abandoned challenge is %v, want failed", m["status"])
		}
	}
}

func TestPollingKeepsAClaimAlive(t *testing.T) {
	hn := newBridgeHarness(t)
	id := newChallenge(hn, "long job")
	hn.req("POST", "/api/bridge/challenges/"+id+"/claim", "agent", nil)
	for i := 0; i < 4; i++ { // two hours of silence from the engine, but the worker keeps polling
		hn.advance(claimStale - time.Minute)
		if c, _ := hn.req("GET", "/api/bridge/inbox", "agent", nil); c != 200 {
			t.Fatal("inbox failed")
		}
	}
	_, st := hn.req("GET", "/api/bridge", "pc", nil)
	for _, c := range st["challenges"].([]any) {
		if m := c.(map[string]any); m["id"] == id && m["status"] != "working" {
			t.Errorf("a live worker's claim went stale: %v", m["status"])
		}
	}
}

func TestStreamAndActionRequestLimits(t *testing.T) {
	hn := newBridgeHarness(t)
	var cancels []func()
	for i := 0; i < maxStreams; i++ {
		ch, cancel := hn.app.bridge.subscribe()
		if ch == nil {
			t.Fatalf("stream %d refused early", i)
		}
		cancels = append(cancels, cancel)
	}
	if ch, _ := hn.app.bridge.subscribe(); ch != nil {
		t.Error("stream past the cap was accepted")
	}
	if c, _ := hn.req("GET", "/api/bridge/stream", "pc", nil); c != 429 {
		t.Errorf("stream over the cap: %d, want 429", c)
	}
	for _, cancel := range cancels {
		cancel()
	}
	var last int
	for i := 0; i < maxActReqs+3; i++ {
		last, _ = hn.req("POST", "/api/bridge/actions/say-hi/request", "agent", nil)
	}
	if last != 429 {
		t.Errorf("unattended action flood: last status %d, want 429", last)
	}
	hn.advance(11 * time.Minute)
	if c, _ := hn.req("POST", "/api/bridge/actions/say-hi/request", "agent", nil); c != 200 {
		t.Errorf("after the window: %d, want 200", c)
	}
}

func TestChallengeInputRules(t *testing.T) {
	hn := newBridgeHarness(t)
	if c, _ := hn.req("POST", "/api/bridge/challenges", "pc", map[string]string{"title": "x", "repo": "../../etc"}); c != 400 {
		t.Errorf("repo given as a path: %d, want 400", c)
	}
	if c, _ := hn.req("POST", "/api/bridge/challenges", "pc", map[string]string{"title": "  ", "repo": "lucille"}); c != 400 {
		t.Errorf("empty title: %d, want 400", c)
	}
	newChallenge(hn, "Same Title")
	if c, o := hn.req("POST", "/api/bridge/challenges", "pc", map[string]string{"title": "same   title!", "repo": "lucille"}); c != 400 || !strings.Contains(o["error"].(string), "duplicate") {
		t.Errorf("duplicate: %d %v", c, o)
	}
	for i := 0; i < maxOpenCh; i++ {
		hn.req("POST", "/api/bridge/challenges", "pc", map[string]string{"title": "filler " + strings.Repeat("x", i+1), "repo": "lucille"})
	}
	if c, _ := hn.req("POST", "/api/bridge/challenges", "pc", map[string]string{"title": "one too many", "repo": "lucille"}); c != 409 {
		t.Errorf("past the open limit: %d, want 409", c)
	}
}

// ── transcript ──────────────────────────────────────────────────

func TestTranscriptIsScrubbedAndPersistent(t *testing.T) {
	hn := newBridgeHarness(t)
	b := hn.app.bridge
	b.emit(BridgeEvent{Who: "boltz\x1b[31m", Kind: "term", Text: "line1\nline2\r\n\x1b[2Jred\u202etxt.exe\u2029end"})
	b.emit(BridgeEvent{Who: "you", Kind: "say", Text: "keep\nthe newline"})
	b.emit(BridgeEvent{Who: "boltz", Kind: "term", Text: strings.Repeat("é", maxEventText+50)})
	evs := b.eventsSince(0, 0)
	if len(evs) != 3 || evs[0].Seq != 1 || evs[2].Seq != 3 {
		t.Fatalf("events = %+v", evs)
	}
	if got := evs[0].Text; strings.ContainsAny(got, "\x1b\r\n\u202e\u2029") {
		t.Errorf("terminal line kept control characters: %q", got)
	}
	if evs[0].Who != "boltz[31m" {
		t.Errorf("who = %q", evs[0].Who) // the ESC is gone; the harmless printable remainder stays
	}
	if !strings.Contains(evs[1].Text, "\n") {
		t.Errorf("a chat message lost its newline: %q", evs[1].Text)
	}
	if n := len([]rune(evs[2].Text)); n > maxEventText+1 || !strings.HasSuffix(evs[2].Text, "…") {
		t.Errorf("long line not truncated: %d runes", n)
	}
	// a restart continues the numbering and replays history
	b2, _ := newBridge(hn.dir, hn.app.cfg.Bridge)
	if ev := b2.emit(BridgeEvent{Who: "you", Kind: "say", Text: "after restart"}); ev.Seq != 4 {
		t.Errorf("seq after restart = %d, want 4", ev.Seq)
	}
	if got := b2.eventsSince(2, 0); len(got) != 2 {
		t.Errorf("eventsSince(2) = %d events, want 2", len(got))
	}
	// the terminal view scrubs again, in case the file was edited by hand
	line := formatEvent(BridgeEvent{Seq: 1, TS: time.Now(), Who: "boltz", Text: "ok\x1b]52;c;ZXZpbA==\x07"}, false)
	if strings.ContainsAny(line, "\x1b\x07") {
		t.Errorf("formatEvent let an escape through: %q", line)
	}
}

func TestStreamReplaysThenFollows(t *testing.T) {
	hn := newBridgeHarness(t)
	b := hn.app.bridge
	b.emit(BridgeEvent{Who: "you", Kind: "say", Text: "before"})
	srv := httptest.NewServer(hn.h) // httptest dials from loopback, so this is the PC
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/bridge/stream?since=0", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	seen := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data: ") {
				seen <- sc.Text()
			}
		}
	}()
	expect := func(sub string) {
		t.Helper()
		select {
		case l := <-seen:
			if !strings.Contains(l, sub) {
				t.Fatalf("got %q, want it to contain %q", l, sub)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no event containing %q", sub)
		}
	}
	expect("before")
	b.emit(BridgeEvent{Who: "boltz", Kind: "term", Text: "live one"})
	expect("live one")
}

// ── Lucille answering ───────────────────────────────────────────

func TestLucilleReplies(t *testing.T) {
	hn := newBridgeHarness(t)
	hn.llmOut = func(system, user string) (string, error) {
		if system != bridgeChatSystem {
			t.Errorf("reply used the wrong system prompt")
		}
		return "On it.", nil
	}
	hn.req("POST", "/api/bridge/say", "phone", map[string]string{"text": "hello Lucille", "to": "lucille"})
	eventually(t, "Lucille's reply", func() bool { return len(hn.texts("say")) >= 2 })
	if got := hn.texts("say"); got[1] != "lucille: On it." {
		t.Errorf("transcript = %v", got)
	}
	// "to boltz" is for Dr. Boltz, not for Lucille
	before := len(hn.texts("say"))
	hn.req("POST", "/api/bridge/say", "pc", map[string]string{"text": "boltz, status?", "to": "boltz"})
	time.Sleep(50 * time.Millisecond)
	if n := len(hn.texts("say")); n != before+1 {
		t.Errorf("Lucille answered a message addressed to Boltz (%d events)", n)
	}
	// no model: a person gets told, the agent gets silence
	hn.llmOut = nil
	hn.req("POST", "/api/bridge/say", "pc", map[string]string{"text": "anyone there?", "to": "lucille"})
	eventually(t, "the no-model note", func() bool {
		for _, s := range hn.texts("say") {
			if strings.Contains(s, "can't reach a language model") {
				return true
			}
		}
		return false
	})
	n := len(hn.texts("say"))
	hn.req("POST", "/api/bridge/say", "agent", map[string]string{"text": "ping", "to": "lucille"})
	time.Sleep(80 * time.Millisecond)
	if got := len(hn.texts("say")); got != n+1 {
		t.Errorf("Lucille replied to the agent with no model (%d events, want %d)", got, n+1)
	}
}

func TestMachinesCannotChatForever(t *testing.T) {
	hn := newBridgeHarness(t)
	var replies atomic.Int32
	hn.llmOut = func(system, user string) (string, error) { replies.Add(1); return "ack", nil }
	for i := 0; i < 10; i++ {
		hn.req("POST", "/api/bridge/say", "agent", map[string]string{"text": "again", "to": "lucille"})
		time.Sleep(30 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	// The streak counts both sides' messages, so a machine-only exchange stops after
	// maxMachineStreak of them: two round trips, never ten.
	n := replies.Load()
	if n == 0 || n > maxMachineStreak/2+1 {
		t.Fatalf("Lucille answered a machine-only exchange %d times; want 1..%d", n, maxMachineStreak/2+1)
	}
	// a person speaking resets it
	hn.req("POST", "/api/bridge/say", "pc", map[string]string{"text": "carry on", "to": "all"})
	eventually(t, "a reply once a person spoke", func() bool { return replies.Load() > n })
}

// ── Lucille's challenges ────────────────────────────────────────

func TestGenerateChallenges(t *testing.T) {
	hn := newBridgeHarness(t)
	hn.llmOut = func(system, user string) (string, error) {
		if system != challengeSystem {
			t.Errorf("wrong system prompt for generation")
		}
		for _, banned := range []string{"mail", "news", "calendar"} {
			if strings.Contains(strings.ToLower(user), banned) {
				t.Errorf("the generator prompt mentions %q; untrusted sources must stay out", banned)
			}
		}
		return "Sure! ```json\n" + `[
		 {"title":"Add a retry to the feed fetch","body":"Acceptance: go test passes.","repo":"lucille","size":"evening"},
		 {"title":"Escape-sequence fix \u001b[31m","body":"b","repo":"boltz","size":"weekend"},
		 {"title":"Targets a path, not a repo","body":"b","repo":"/etc","size":"evening"},
		 {"title":"add a RETRY to the feed fetch!","body":"dup","repo":"lucille","size":"evening"},
		 {"title":"Fourth good one","body":"b","repo":"lucille","size":"sprint"}
		]` + "\n```", nil
	}
	if c, _ := hn.req("POST", "/api/bridge/generate", "agent", map[string]int{"count": 3}); c != 403 {
		t.Fatalf("agent generating its own work: %d, want 403", c)
	}
	c, o := hn.req("POST", "/api/bridge/generate", "phone", map[string]int{"count": 3})
	hn.mustOK(c, o, 200)
	made := o["challenges"].([]any)
	if o["source"] != "model" || len(made) != 3 {
		t.Fatalf("source=%v made=%d (%v)", o["source"], len(made), made)
	}
	for _, m := range made {
		cm := m.(map[string]any)
		if cm["source"] != "lucille" || cm["status"] != "open" || strings.ContainsRune(cm["title"].(string), 0x1b) {
			t.Errorf("bad generated challenge %v", cm)
		}
		if cm["repo"] != "lucille" && cm["repo"] != "boltz" {
			t.Errorf("generated challenge for repo %v", cm["repo"])
		}
	}
	// no model: curated fallbacks, and asking again does not duplicate them
	hn.llmOut = nil
	c, o = hn.req("POST", "/api/bridge/generate", "pc", map[string]int{"count": 5})
	hn.mustOK(c, o, 200)
	if o["source"] != "curated" || len(o["challenges"].([]any)) == 0 {
		t.Fatalf("fallback: %v", o)
	}
	_, o = hn.req("POST", "/api/bridge/generate", "pc", map[string]int{"count": 5})
	if len(o["challenges"].([]any)) != 0 {
		t.Errorf("curated challenges were queued twice: %v", o["challenges"])
	}
	if c, _ := hn.req("POST", "/api/bridge/generate", "pc", map[string]any{"count": 1, "repo": "nope"}); c != 400 {
		t.Errorf("unknown repo filter: %d, want 400", c)
	}
}

func TestCuratedChallengesTargetConfiguredRepos(t *testing.T) {
	for _, c := range curatedChallenges {
		if _, ok := defaultBridgeConfig().repo(c.Repo); !ok {
			t.Errorf("curated challenge %q targets %q, which is not a default repo", c.Title, c.Repo)
		}
		if len(c.Body) < 40 || c.Title == "" {
			t.Errorf("curated challenge %q is too thin", c.Title)
		}
	}
}

// ── actions Dr. Boltz asks for ──────────────────────────────────

func TestAgentCannotRunActionsDirectly(t *testing.T) {
	hn := newBridgeHarness(t)
	if c, _ := hn.req("POST", "/api/actions/say-hi/run", "agent", nil); c != 403 {
		t.Errorf("agent on the direct run route: %d, want 403", c)
	}
	if c, _ := hn.req("POST", "/api/actions/say-hi/run", "stale", nil); c != 403 {
		t.Errorf("stale token on the direct run route: %d, want 403", c)
	}
	if c, o := hn.req("POST", "/api/actions/say-hi/run", "pc", nil); c != 200 || o["ok"] != true {
		t.Errorf("a person running it: %d %v", c, o)
	}
}

func TestActionRequests(t *testing.T) {
	hn := newBridgeHarness(t)

	// agent_auto: runs now, no tap
	c, o := hn.req("POST", "/api/bridge/actions/say-hi/request", "agent", map[string]string{"reason": "greet"})
	hn.mustOK(c, o, 200)
	ap := o["approval"].(map[string]any)
	if ap["status"] != "done" || !strings.Contains(ap["result"].(map[string]any)["output"].(string), "hi") {
		t.Fatalf("agent_auto action: %v", ap)
	}

	// everything else waits for a person
	c, o = hn.req("POST", "/api/bridge/actions/ask-first/request", "agent", map[string]string{"reason": "need it"})
	hn.mustOK(c, o, 200)
	ap = o["approval"].(map[string]any)
	id := ap["id"].(string)
	if ap["status"] != "pending" || ap["result"] != nil {
		t.Fatalf("expected pending, got %v", ap)
	}
	if c, _ := hn.req("POST", "/api/bridge/approvals/"+id+"/decision", "agent", map[string]bool{"approve": true}); c != 403 {
		t.Errorf("agent approving its own request: %d, want 403", c)
	}
	_, got := hn.req("GET", "/api/bridge/approvals/"+id, "agent", nil)
	if got["approval"].(map[string]any)["status"] != "pending" {
		t.Errorf("request ran without approval: %v", got)
	}
	c, o = hn.req("POST", "/api/bridge/approvals/"+id+"/decision", "phone", map[string]bool{"approve": true})
	hn.mustOK(c, o, 200)
	ap = o["approval"].(map[string]any)
	if ap["status"] != "done" || ap["decided_by"] != "My Phone" || !strings.Contains(ap["result"].(map[string]any)["output"].(string), "asked") {
		t.Fatalf("after approval: %v", ap)
	}
	if c, _ := hn.req("POST", "/api/bridge/approvals/"+id+"/decision", "phone", map[string]bool{"approve": true}); c != 409 {
		t.Errorf("approving twice: %d, want 409", c)
	}

	// denial never runs it
	_, o = hn.req("POST", "/api/bridge/actions/ask-first/request", "agent", map[string]string{"reason": "again"})
	id2 := o["approval"].(map[string]any)["id"].(string)
	_, o = hn.req("POST", "/api/bridge/approvals/"+id2+"/decision", "pc", map[string]bool{"approve": false})
	if o["approval"].(map[string]any)["status"] != "denied" || o["approval"].(map[string]any)["result"] != nil {
		t.Errorf("denied request: %v", o)
	}

	// a request nobody answers expires, and an expired one cannot be approved
	_, o = hn.req("POST", "/api/bridge/actions/ask-first/request", "agent", map[string]string{"reason": "slow"})
	id3 := o["approval"].(map[string]any)["id"].(string)
	hn.advance(approvalTTL + time.Minute)
	if c, _ := hn.req("POST", "/api/bridge/approvals/"+id3+"/decision", "pc", map[string]bool{"approve": true}); c != 409 {
		t.Errorf("approving an expired request: %d, want 409", c)
	}

	// destructive actions are off the table for the agent, whatever the config says
	if c, _ := hn.req("POST", "/api/bridge/actions/nuke/request", "agent", nil); c != 403 {
		t.Errorf("destructive request: %d, want 403", c)
	}
	if c, _ := hn.req("POST", "/api/bridge/actions/nope/request", "agent", nil); c != 404 {
		t.Errorf("unknown action: %d, want 404", c)
	}
	// and it cannot bury the phone in requests
	var last int
	for i := 0; i < maxPendingAppr+2; i++ {
		last, _ = hn.req("POST", "/api/bridge/actions/ask-first/request", "agent", map[string]string{"reason": "spam"})
	}
	if last != 429 {
		t.Errorf("request flood: last status %d, want 429", last)
	}
	// every run is in the audit log
	raw, _ := os.ReadFile(filepath.Join(hn.dir, "actions.log"))
	if n := strings.Count(string(raw), `"action_id"`); n < 2 {
		t.Errorf("audit log has %d entries, want at least 2", n)
	}
}

func TestAgentAutoNeverOnDestructive(t *testing.T) {
	got := normalizeActions([]Action{{ID: "boom", Command: []string{"/usr/bin/true"}, Destructive: true, AgentAuto: true}})
	if len(got) != 1 || got[0].AgentAuto {
		t.Errorf("destructive action kept agent_auto: %+v", got)
	}
}

func TestSudoersTemplateExplainsEachLine(t *testing.T) {
	var sb strings.Builder
	if err := runSudoersTemplate(defaultActions(), &sb); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{
		"# kernel-warnings: Dr. Boltz may run this unattended",
		"NOPASSWD: /usr/bin/dmesg --level=err,warn --ctime",
		"# drop-ram-caches: a person approves each use",
		"# reboot-pc: DESTRUCTIVE",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("template is missing %q:\n%s", want, out)
		}
	}
}

// ── config ──────────────────────────────────────────────────────

func TestNormalizeBridge(t *testing.T) {
	got := normalizeBridge(BridgeConfig{MaxBudgetUSD: 500, MaxMinutes: -3, Repos: []BridgeRepo{
		{Name: "ok", Path: "/a", WorkDir: "/w"},
		{Name: "Bad Name", Path: "/a", WorkDir: "/w"},
		{Name: "rel", Path: "a", WorkDir: "/w"},
		{Name: "escape", Path: "/a", WorkDir: "/w", Scope: "../../etc"},
		{Name: "ok", Path: "/b", WorkDir: "/w"},
		{Name: "scoped", Path: "/a", WorkDir: "/w", Scope: "/30-source/x/"},
	}})
	if got.MaxBudgetUSD != 2 || got.MaxMinutes != 20 {
		t.Errorf("limits not clamped to defaults: %+v", got)
	}
	var names []string
	for _, r := range got.Repos {
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "ok,scoped" {
		t.Errorf("repos kept = %v, want ok,scoped", names)
	}
	if r, _ := got.repo("scoped"); r.Scope != "30-source/x" {
		t.Errorf("scope = %q", r.Scope)
	}
}

func TestDefaultBridgeReposAreValid(t *testing.T) {
	d := normalizeBridge(defaultBridgeConfig())
	if len(d.Repos) != 2 {
		t.Fatalf("default repos = %+v", d.Repos)
	}
	for _, r := range d.Repos {
		if r.Verify == "" || r.Desc == "" {
			t.Errorf("repo %s needs a verify command and a description", r.Name)
		}
	}
}
