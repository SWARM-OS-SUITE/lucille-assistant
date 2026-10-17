package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const sampleBuildList = `# MASTER BUILD LIST

## SW: SWARM OS  (owner: x)
| ID | P | Item | Status | Notes / source |
|---|---|---|---|---|
| SW-01 | P1 | Boot the ISO | WIP | note |
| SW-02 | P0 | **Fix** the ` + "`crash`" + ` | TODO (user) | |
| SW-03 | P2 | Old thing | DONE, verified | |
| SW-04 | P2 | A maybe | IDEA | |
| SW-05 | P1 | Leaky | TODO | api_key = sk-abcdefghijklmnopqrstuvwxyz |

## LA: Lucille Assistant  (owner: SWARM AI)
| ID | P | Item | Status | Notes / source |
|---|---|---|---|---|
| LA-01 | P1 | Approval service | PARTIAL | |
| LA-03 | P2 | Version stamp | TODO | |

---
## Idea inbox (append only; date and who)
- 2026-10-06 user: first idea
- 2026-10-07 user: second idea

## Retired by this file
See the log.
`

// ── reading files ───────────────────────────────────────────────

func writeBriefFile(t *testing.T, p, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBriefReaderGuards(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeBriefFile(t, filepath.Join(root, "ok.md"), "hello")
	writeBriefFile(t, filepath.Join(root, "notes.txt"), "text")
	writeBriefFile(t, filepath.Join(root, ".env"), "KEY=secret")
	writeBriefFile(t, filepath.Join(root, "secrets.md"), "x")
	writeBriefFile(t, filepath.Join(root, "server.key"), "x")
	writeBriefFile(t, filepath.Join(root, "code.go"), "x")
	writeBriefFile(t, filepath.Join(root, ".hidden", "a.md"), "x")
	writeBriefFile(t, filepath.Join(outside, "outside.md"), "do not read")
	writeBriefFile(t, filepath.Join(outside, "outside.pem"), "do not read")
	if err := os.Symlink(filepath.Join(outside, "outside.md"), filepath.Join(root, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside.pem"), filepath.Join(root, "linkpem.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "secrets.md"), filepath.Join(root, "innocent.md")); err != nil {
		t.Fatal(err)
	}
	cfg := normalizeBriefConfig(BriefConfig{Roots: []string{root}})
	rd := newBriefReader(cfg)

	cases := []struct {
		path, source string
	}{
		{filepath.Join(root, "ok.md"), "pc"},
		{filepath.Join(root, "notes.txt"), "pc"},
		{filepath.Join(root, "nope.md"), "missing"},
		{filepath.Join(root, ".env"), "blocked"},
		{filepath.Join(root, "secrets.md"), "blocked"},
		{filepath.Join(root, "server.key"), "blocked"},
		{filepath.Join(root, "code.go"), "blocked"},
		{filepath.Join(root, ".hidden", "a.md"), "blocked"},
		{filepath.Join(root, "innocent.md"), "blocked"},   // harmless name, but a link to a secrets file inside the root
		{filepath.Join(root, "link.md"), "blocked"},       // symlink out of the root
		{filepath.Join(root, "linkpem.md"), "blocked"},    // symlink to a key file
		{filepath.Join(outside, "outside.md"), "blocked"}, // not under any root
		{root + "/../" + filepath.Base(outside) + "/outside.md", "blocked"},
		{"relative.md", "blocked"},
		{"/etc/passwd", "blocked"},
		{"/etc/hostname.md", "missing"},
	}
	for _, c := range cases {
		text, f := rd.read(c.path)
		if f.Source != c.source {
			t.Errorf("%s: source %q, want %q (%s)", c.path, f.Source, c.source, f.Reason)
		}
		if c.source != "pc" && text != nil {
			t.Errorf("%s: returned text for a %s file", c.path, c.source)
		}
	}
	if text, f := rd.read(filepath.Join(root, "ok.md")); string(text) != "hello" || f.Bytes != 5 || f.Modified.IsZero() {
		t.Errorf("ok.md: %q %+v", text, f)
	}
}

func TestBriefReaderAliasAndGitRef(t *testing.T) {
	real := t.TempDir()
	writeBriefFile(t, filepath.Join(real, "src", "README.md"), "readme")
	canon := filepath.Join(t.TempDir(), "lucillebuild") // does not exist: the partition is mounted elsewhere
	cfg := normalizeBriefConfig(BriefConfig{
		Roots:   []string{canon},
		Aliases: map[string]string{canon: real},
		Git:     []GitRoot{{Root: "/mnt/Lucy", Repo: "o/r", Branch: "main"}},
	})
	rd := newBriefReader(cfg)
	if got := cfg.liveFor(filepath.Join(canon, "src", "README.md")); got != filepath.Join(real, "src", "README.md") {
		t.Errorf("liveFor should name the path that exists on this boot, got %s", got)
	}
	text, f := rd.read(filepath.Join(canon, "src", "README.md"))
	if f.Source != "pc" || string(text) != "readme" {
		t.Fatalf("alias read failed: %+v", f)
	}
	if f.Git != nil {
		t.Errorf("a file outside any git root must carry no git address: %+v", f.Git)
	}
	_, f = rd.read("/mnt/Lucy/00-CURRENT/MASTER_BUILD_LIST.md")
	if f.Git == nil || f.Git.Repo != "o/r" || f.Git.Rel != "00-CURRENT/MASTER_BUILD_LIST.md" || f.Git.Branch != "main" {
		t.Errorf("git ref wrong: %+v", f.Git)
	}
}

func TestBriefConfigRejectsBadGitAndRoots(t *testing.T) {
	c := normalizeBriefConfig(BriefConfig{
		Roots: []string{"relative", "/"},
		Git: []GitRoot{
			{Root: "/mnt/x", Repo: "../../etc", Branch: "main"},
			{Root: "/mnt/x", Repo: "o/r", Branch: "main; rm"},
			{Root: "/mnt/x", Repo: "o/r", Branch: "main"},
		},
	})
	if len(c.Roots) == 0 || c.Roots[0] == "relative" || c.Roots[0] == "/" {
		t.Errorf("bad roots survived: %v", c.Roots)
	}
	if len(c.Git) != 1 {
		t.Errorf("want only the valid git root, got %+v", c.Git)
	}
}

// ── the tracker ─────────────────────────────────────────────────

func TestParseTrackerAndSummary(t *testing.T) {
	items, ok := parseTracker(sampleBuildList, "SW")
	if !ok || len(items) != 5 {
		t.Fatalf("ok=%v items=%+v", ok, items)
	}
	tr := summarizeTracker("MASTER_BUILD_LIST.md", items)
	if tr.Total != 5 || tr.Done != 1 {
		t.Errorf("total/done = %d/%d", tr.Total, tr.Done)
	}
	var ids []string
	for _, it := range tr.Open {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, ",") != "SW-02,SW-01,SW-05" { // P0 first; DONE and IDEA are not open
		t.Errorf("open items %v", ids)
	}
	if tr.Open[0].Item != "Fix the crash" {
		t.Errorf("markdown not stripped: %q", tr.Open[0].Item)
	}
	if _, ok := parseTracker(sampleBuildList, "ZZ"); ok {
		t.Error("found a section that does not exist")
	}
	// LA must not pick up SW rows or the inbox below it.
	la, _ := parseTracker(sampleBuildList, "LA")
	if len(la) != 2 || la[0].ID != "LA-01" {
		t.Errorf("LA rows: %+v", la)
	}
}

func TestTrackerStripsMarkdownEmphasis(t *testing.T) {
	items, _ := parseTracker("## SW: x\n| ID | P | Item | Status |\n|---|---|---|---|\n| SW-1 | P1 | enable *Remote PC → Alerts* and **bold** `code` | TODO |\n", "SW")
	if len(items) != 1 || items[0].Item != "enable Remote PC → Alerts and bold code" {
		t.Fatalf("%+v", items)
	}
}

func TestTrackerRedactsSecrets(t *testing.T) {
	items, _ := parseTracker("## SW: x\n| ID | P | Item | Status |\n|---|---|---|---|\n| SW-1 | P1 | token: abcdefghijkl and sk-abcdefghijklmnopqrstuvwxyz | TODO |\n", "SW")
	if len(items) != 1 || strings.Contains(items[0].Item, "abcdefghijkl") {
		t.Fatalf("secret leaked: %+v", items)
	}
}

// ── suggestions ─────────────────────────────────────────────────

func TestBriefingSuggestionsFromTheRealSeeds(t *testing.T) {
	seeds, err := builtinBriefings()
	if err != nil || len(seeds) != 4 {
		t.Fatalf("seeds: %v %d", err, len(seeds))
	}
	for _, b := range seeds {
		lines := briefingSuggestions(b.Body)
		if len(lines) != 3 {
			t.Errorf("%s: parsed %d suggestions, want 3: %q", b.ID, len(lines), lines)
		}
		for _, l := range lines {
			title, why := splitSuggestion(l)
			if title == "" || len(title) > 125 || why == "" {
				t.Errorf("%s: bad split %q / %q", b.ID, title, why)
			}
		}
	}
}

func TestSplitSuggestionAndSize(t *testing.T) {
	title, why := splitSuggestion("Boot-health chip: show green / amber / red from one endpoint. About half a day.")
	if title != "Boot-health chip" || !strings.HasPrefix(why, "Show green") {
		t.Errorf("%q | %q", title, why)
	}
	if sizeFor("About 2 h.") != "evening" || sizeFor("about half a day") != "evening" || sizeFor("About 9 h") != "weekend" || sizeFor("a week of work") != "sprint" {
		t.Error("size guess wrong")
	}
	if suggestionID("a", "Same Title!") != suggestionID("a", "same   title") || suggestionID("a", "x") == suggestionID("b", "x") {
		t.Error("ids must ignore case/punctuation and differ per project")
	}
}

func TestSuggestionCommandIsOneSafeWord(t *testing.T) {
	dir := t.TempDir()
	hn := newBridgeHarness(t)
	hn.app.cfg.Brief.Dirs["lucille-assistant"] = dir
	nasty := BriefSuggestion{Title: "x'; touch " + filepath.Join(dir, "PWNED") + " #", Why: "$(touch " + filepath.Join(dir, "PWNED2") + ")\n`touch " + filepath.Join(dir, "PWNED3") + "`"}
	cmd := hn.app.suggestionCommand("lucille-assistant", "Lucille Assistant", nasty)
	if strings.ContainsAny(cmd, "\n\r") {
		t.Fatalf("command has a newline: %q", cmd)
	}
	// Run it with a stand-in "claude" that prints how many arguments it received.
	script := "claude() { printf '%s\\n' \"$#\"; }; " + cmd
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("the prompt must be exactly one argument, got %q", out)
	}
	for _, n := range []string{"PWNED", "PWNED2", "PWNED3"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Fatalf("shell injection created %s", n)
		}
	}
}

// ── the project markdown ────────────────────────────────────────

func TestInsertInbox(t *testing.T) {
	got := insertInbox(sampleBuildList, "Idea inbox", "- NEW")
	want := "- 2026-10-07 user: second idea\n- NEW\n\n## Retired"
	if !strings.Contains(got, want) {
		t.Errorf("not inserted at the end of the section:\n%s", got)
	}
	if strings.Count(got, "\n") != strings.Count(sampleBuildList, "\n")+1 {
		t.Error("inserted more than one line")
	}
	empty := insertInbox("# Title\n", "Idea inbox", "- NEW")
	if empty != "# Title\n\n## Idea inbox (append only; date and who)\n- NEW\n" {
		t.Errorf("no-section case: %q", empty)
	}
	noNL := insertInbox("# Title", "Idea inbox", "- NEW")
	if !strings.HasSuffix(noNL, "- NEW\n") || !strings.Contains(noNL, "# Title\n\n## Idea") {
		t.Errorf("no trailing newline case: %q", noNL)
	}
}

func TestAppendToMDIsIdempotentAndConcurrencySafe(t *testing.T) {
	hn := newBridgeHarness(t)
	root := t.TempDir()
	md := filepath.Join(root, "00-CURRENT", "MASTER_BUILD_LIST.md")
	writeBriefFile(t, md, sampleBuildList)
	hn.app.cfg.Brief = normalizeBriefConfig(BriefConfig{Roots: []string{root}, MD: md})
	rd := newBriefReader(hn.app.cfg.Brief)

	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			marker := "[sg-0000000" + string(rune('a'+i)) + "]"
			if _, err := hn.app.appendToMD(rd, "- line "+marker, marker, nil); err != nil {
				t.Errorf("append %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	data, _ := os.ReadFile(md)
	for i := 0; i < 12; i++ {
		if n := strings.Count(string(data), "[sg-0000000"+string(rune('a'+i))+"]"); n != 1 {
			t.Errorf("line %d appears %d times", i, n)
		}
	}
	if !strings.Contains(string(data), "## Retired by this file\nSee the log.") {
		t.Error("the rest of the file was damaged")
	}
	added, err := hn.app.appendToMD(rd, "- again [sg-0000000a]", "[sg-0000000a]", nil)
	if err != nil || added {
		t.Errorf("a marker that is already there must not be added again: added=%v err=%v", added, err)
	}
	if st, _ := os.Stat(md); st.Mode().Perm() != 0o644 {
		t.Errorf("file mode changed to %v", st.Mode().Perm())
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(md), ".lucille-md-*"))
	if len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

func TestAppendToMDRefusesFilesOutsideTheRoots(t *testing.T) {
	hn := newBridgeHarness(t)
	root, other := t.TempDir(), t.TempDir()
	md := filepath.Join(other, "list.md")
	writeBriefFile(t, md, "# x\n")
	hn.app.cfg.Brief = normalizeBriefConfig(BriefConfig{Roots: []string{root}, MD: md})
	if _, err := hn.app.appendToMD(newBriefReader(hn.app.cfg.Brief), "- x [sg-00000000]", "[sg-00000000]", nil); err == nil {
		t.Fatal("wrote outside the allowed roots")
	}
	if b, _ := os.ReadFile(md); string(b) != "# x\n" {
		t.Fatalf("file was changed: %q", b)
	}
}

// ── the HTTP surface ────────────────────────────────────────────

func briefHarness(t *testing.T) (*bridgeHarness, string) {
	t.Helper()
	hn := newBridgeHarness(t)
	root := t.TempDir()
	md := filepath.Join(root, "00-CURRENT", "MASTER_BUILD_LIST.md")
	writeBriefFile(t, md, sampleBuildList)
	writeBriefFile(t, filepath.Join(root, "CLAUDE.md"), "# notes")
	hn.app.cfg.Brief = normalizeBriefConfig(BriefConfig{
		Roots: []string{root}, MD: md,
		Git:         []GitRoot{{Root: root, Repo: "o/r", Branch: "main"}},
		ProjectRepo: map[string]string{"proj-la": "lucille"},
		Dirs:        map[string]string{"proj-la": root, "proj-sw": root},
	})
	now := time.Now()
	for _, in := range []struct {
		id, code string
		b        BriefingInput
	}{
		{"proj-la", "LA", BriefingInput{Title: "Proj LA", Project: "LA", Summary: "sum",
			Body:      "STATUS\n- ok\n\n3 SUGGESTIONS\n1. Alpha idea: do the alpha thing. About 2 h.\n2. Beta idea: do beta.\n3. Gamma: gamma.\n\nREFERENCE\n- x",
			ReadFirst: []string{md, filepath.Join(root, "CLAUDE.md"), filepath.Join(root, "gone.md"), filepath.Join(root, ".env")}}},
		{"proj-sw", "SW", BriefingInput{Title: "Proj SW", Project: "SW", Summary: "sw sum", Body: "no suggestions here", ReadFirst: []string{md}}},
	} {
		b := Briefing{ID: in.id, Title: in.b.Title, Project: in.b.Project, Summary: in.b.Summary, Body: in.b.Body, ReadFirst: in.b.ReadFirst, Created: now, Updated: now}
		if err := hn.app.store.saveBriefing(b); err != nil {
			t.Fatal(err)
		}
	}
	return hn, md
}

func TestProjectBriefEndToEnd(t *testing.T) {
	hn, _ := briefHarness(t)
	code, out := hn.req("GET", "/api/projectbrief", "pc", nil)
	hn.mustOK(code, out, 200)
	if out["mode"] != "pc" {
		t.Errorf("mode %v", out["mode"])
	}
	if md := out["md"].(map[string]any); md["writable"] != true {
		t.Errorf("md should be writable: %v", md)
	}
	var la, sw map[string]any
	for _, p := range out["projects"].([]any) {
		pm := p.(map[string]any)
		switch pm["id"] {
		case "proj-la":
			la = pm
		case "proj-sw":
			sw = pm
		}
	}
	if la == nil || sw == nil {
		t.Fatalf("projects missing: %v", out["projects"])
	}
	// files: read, read, missing, blocked — and a git address only where a git root covers it
	files := la["files"].([]any)
	var sources []string
	for _, f := range files {
		sources = append(sources, f.(map[string]any)["source"].(string))
	}
	if strings.Join(sources, ",") != "pc,pc,missing,blocked" {
		t.Errorf("sources %v", sources)
	}
	if files[0].(map[string]any)["git"] == nil {
		t.Error("the build list should carry its git address")
	}
	tr := la["tracker"].(map[string]any)
	if tr["source"] != "pc" || len(tr["open"].([]any)) != 2 {
		t.Errorf("LA tracker: %v", tr)
	}
	// suggestions: from the briefing, with a safe command, repo and send state
	sugs := la["suggestions"].([]any)
	if len(sugs) != 3 || la["suggestions_source"] != "briefing" {
		t.Fatalf("suggestions: %v %v", la["suggestions_source"], sugs)
	}
	s0 := sugs[0].(map[string]any)
	if s0["title"] != "Alpha idea" || !strings.HasPrefix(s0["command"].(string), "cd '") || !strings.Contains(s0["command"].(string), "claude '") {
		t.Errorf("first suggestion: %v", s0)
	}
	if la["can_send"] != true || la["repo"] != "lucille" {
		t.Errorf("LA should be sendable: %v %v", la["can_send"], la["send_note"])
	}
	if sw["can_send"] != false || sw["send_note"] == "" {
		t.Errorf("SW has no Boltz repo and must say why: %v", sw["send_note"])
	}
	// no suggestion list in the briefing -> top open tracker items, never invented ones
	if sw["suggestions_source"] != "tracker" || len(sw["suggestions"].([]any)) != 3 {
		t.Errorf("SW fallback suggestions: %v %v", sw["suggestions_source"], sw["suggestions"])
	}
	for _, s := range sw["suggestions"].([]any) {
		if strings.Contains(s.(map[string]any)["title"].(string), "abcdefghijklmnop") {
			t.Error("secret reached a suggestion")
		}
	}
}

func TestProjectBriefReportsMissingDriveHonestly(t *testing.T) {
	hn, md := briefHarness(t)
	if err := os.Remove(md); err != nil {
		t.Fatal(err)
	}
	_, out := hn.req("GET", "/api/projectbrief", "pc", nil)
	md0 := out["md"].(map[string]any)
	if md0["writable"] != false {
		t.Error("a missing md cannot be writable")
	}
	for _, p := range out["projects"].([]any) {
		pm := p.(map[string]any)
		if tr := pm["tracker"].(map[string]any); tr["source"] != "none" || len(tr["open"].([]any)) != 0 {
			t.Errorf("%v: invented tracker rows for a missing file: %v", pm["id"], tr)
		}
	}
}

func TestProjectMDEndpoint(t *testing.T) {
	hn, md := briefHarness(t)
	body := map[string]any{"title": "Alpha idea", "why": "do the alpha thing"}
	for _, who := range []string{"agent", "open", "stale"} {
		if code, out := hn.req("POST", "/api/projectbrief/proj-la/md", who, body); code < 400 {
			t.Errorf("%s was allowed to write the project file: %d %v", who, code, out)
		}
	}
	if b, _ := os.ReadFile(md); string(b) != sampleBuildList {
		t.Fatal("a refused request still changed the file")
	}
	code, out := hn.req("POST", "/api/projectbrief/proj-la/md", "phone", body)
	hn.mustOK(code, out, 200)
	if out["added"] != true {
		t.Fatalf("not added: %v", out)
	}
	data, _ := os.ReadFile(md)
	id := suggestionID("proj-la", "Alpha idea")
	if !strings.Contains(string(data), "lucille: LA suggestion from the Lucille brief (unvetted): Alpha idea. do the alpha thing ["+id+"]") {
		t.Errorf("line missing or wrong:\n%s", data)
	}
	code, out = hn.req("POST", "/api/projectbrief/proj-la/md", "phone", body)
	hn.mustOK(code, out, 200)
	if out["already"] != true || strings.Count(readStr(t, md), "["+id+"]") != 1 {
		t.Errorf("second add duplicated: %v", out)
	}
	// the brief now shows it as added
	_, br := hn.req("GET", "/api/projectbrief", "pc", nil)
	found := false
	for _, p := range br["projects"].([]any) {
		for _, s := range p.(map[string]any)["suggestions"].([]any) {
			if sm := s.(map[string]any); sm["id"] == id && sm["in_md"] == true {
				found = true
			}
		}
	}
	if !found {
		t.Error("the brief does not show the suggestion as already added")
	}
	if code, _ := hn.req("POST", "/api/projectbrief/nope/md", "phone", body); code != 404 {
		t.Errorf("unknown project: %d", code)
	}
	if code, _ := hn.req("POST", "/api/projectbrief/proj-la/md", "phone", map[string]any{"title": " "}); code != 400 {
		t.Errorf("empty title: %d", code)
	}
}

func readStr(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProjectBoltzEndpoint(t *testing.T) {
	hn, _ := briefHarness(t)
	body := map[string]any{"title": "Alpha idea", "body": "Done means: go test passes.", "size": "evening", "sid": suggestionID("proj-la", "Alpha idea")}

	for _, who := range []string{"agent", "open", "stale"} {
		if code, out := hn.req("POST", "/api/projectbrief/proj-la/boltz", who, body); code < 400 {
			t.Errorf("%s may not queue code for Dr. Boltz: %d %v", who, code, out)
		}
	}
	if n := len(hn.app.bridge.chal); n != 0 {
		t.Fatalf("a refused request queued %d challenges", n)
	}
	if code, _ := hn.req("POST", "/api/projectbrief/proj-sw/boltz", "phone", body); code != 409 {
		t.Errorf("a project with no Boltz repo must be refused with 409, got %d", code)
	}
	if code, _ := hn.req("POST", "/api/projectbrief/proj-la/boltz", "phone", map[string]any{"title": "x"}); code != 400 {
		t.Errorf("a job without a body must be refused: %d", code)
	}
	code, out := hn.req("POST", "/api/projectbrief/proj-la/boltz", "phone", body)
	hn.mustOK(code, out, 200)
	c := ch(out)
	if c["repo"] != "lucille" || c["title"] != "Alpha idea" || c["source"] != "human" {
		t.Errorf("challenge: %v", c)
	}
	if code, _ := hn.req("POST", "/api/projectbrief/proj-la/boltz", "phone", body); code != 400 {
		t.Errorf("the same job twice must be refused as a duplicate: %d", code)
	}
	// the brief now knows it went to Dr. Boltz
	_, br := hn.req("GET", "/api/projectbrief", "pc", nil)
	for _, p := range br["projects"].([]any) {
		pm := p.(map[string]any)
		if pm["id"] != "proj-la" {
			continue
		}
		s0 := pm["suggestions"].([]any)[0].(map[string]any)
		if s0["sent"] != c["id"] || s0["sent_status"] == "" {
			t.Errorf("sent state missing: %v", s0)
		}
	}
}

func TestProjectThink(t *testing.T) {
	hn, _ := briefHarness(t)
	thinkMu.Lock()
	thinkLast = map[string]time.Time{}
	thinkMu.Unlock()

	if code, _ := hn.req("POST", "/api/projectbrief/proj-la/think", "agent", nil); code < 400 {
		t.Error("Dr. Boltz may not spend the user's model budget")
	}
	// no model: an honest error, and the briefing's suggestions stay
	code, out := hn.req("POST", "/api/projectbrief/proj-la/think", "phone", nil)
	if code != 502 || !strings.Contains(out["error"].(string), "No language model") {
		t.Fatalf("no model: %d %v", code, out)
	}
	if _, ok := hn.app.loadStored("proj-la"); ok {
		t.Fatal("saved suggestions after a failed call")
	}
	thinkMu.Lock()
	thinkLast = map[string]time.Time{}
	thinkMu.Unlock()
	var sawUser string
	hn.llmOut = func(system, user string) (string, error) {
		sawUser = user
		return "Sure!\n[{\"title\":\"Add a nightly digest\",\"why\":\"It saves a morning check.\",\"body\":\"Done means: a test.\",\"size\":\"weekend\"},{\"title\":\"\",\"why\":\"empty\"},{\"title\":\"Second\",\"why\":\"w\",\"body\":\"b\",\"size\":\"bogus\"}]", nil
	}
	code, out = hn.req("POST", "/api/projectbrief/proj-la/think", "phone", nil)
	hn.mustOK(code, out, 200)
	if out["count"].(float64) != 2 {
		t.Errorf("count %v", out["count"])
	}
	if !strings.Contains(sawUser, "LA-01") || strings.Contains(sawUser, "abcdefghijklmnop") {
		t.Errorf("the model prompt should carry open tracker items and no secrets:\n%s", sawUser)
	}
	// a second tap straight away is rate limited
	if code, _ := hn.req("POST", "/api/projectbrief/proj-la/think", "phone", nil); code != 429 {
		t.Errorf("no cooldown: %d", code)
	}
	_, br := hn.req("GET", "/api/projectbrief", "pc", nil)
	for _, p := range br["projects"].([]any) {
		pm := p.(map[string]any)
		if pm["id"] != "proj-la" {
			continue
		}
		if pm["suggestions_source"] != "model" {
			t.Fatalf("model suggestions not used: %v", pm["suggestions_source"])
		}
		sg := pm["suggestions"].([]any)
		if len(sg) != 2 || sg[1].(map[string]any)["size"] != "evening" {
			t.Errorf("suggestions %v", sg)
		}
	}
}

// The four lines claude-lucillebuild wrote to the real Idea inbox on 2026-10-09, without markers.
const realInboxLines = `- 2026-10-09 claude-lucillebuild: SW suggestions from the Lucille briefings (user asked for 3 per project, 2026-10-09, unvetted): (1) boot-health chip on the desktop fed by one /api/health (SW-19); (2) unplugged-drive canary: boot-time job logging missing external mounts + desktop up; (3) ISO release gate: no copy to Ventoy unless the VM boot test passed and the .sha256 exists (SW-10, SW-18).
- 2026-10-09 claude-lucillebuild: RC suggestions from the Lucille briefings (2026-10-09, unvetted): (1) 60-second first-boot self-check screen, PASS/FAIL (disks read-only, Ollama up, model loaded, network off); (2) per-repair plain-text case file (findings, approvals, SMART before/after) on the external drive; (3) boot-time image .sha256 self-verify with a warning.
- 2026-10-09 claude-lucillebuild: CP suggestions from the Lucille briefings (2026-10-09, unvetted): (1) pairing QR (address + one-time code) instead of typing tokens; (2) nightly quick ClamAV scan per node with a push only when something is found (first slice of CP-21); (3) Approve/Deny lock-screen notification actions, biometric still required to approve.
- 2026-10-09 claude-lucillebuild: LA suggestions from the Lucille briefings (2026-10-09, unvetted): (1) nightly self-updating STATUS lines in the briefings from git state + this tracker; (2) morning push of the day's first three scheduled steps; (3) the scheduled steps as Google Calendar events via GoogleWorkspaceService.`

// Titles whose hand-written inbox line is a loose paraphrase (only some of the words survive):
// the matcher is deliberately not that fuzzy. Keep this list short and honest.
var knownParaphraseMiss = map[string]bool{"CP|Scheduled scans, quiet pushes": true, "RC|Stick self-verify": true}

func TestInboxHasRecognisesSuggestionsAlreadyWrittenByHand(t *testing.T) {
	lines := inboxLines("# x\n\n## Idea inbox\n" + realInboxLines + "\n- 2026-10-11 lucille-claude: Bridge hardening: something else\n\n| LA-01 | tracker row, not an inbox line |\n")
	if len(lines) != 5 {
		t.Fatalf("want 5 dated lines, got %d", len(lines))
	}
	seeds, err := builtinBriefings()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range seeds {
		for _, raw := range briefingSuggestions(b.Body) {
			title, _ := splitSuggestion(raw)
			n++
			if !inboxHas(lines, b.Project, title) && !knownParaphraseMiss[b.Project+"|"+title] {
				t.Errorf("%s %q was written to the inbox by hand but is not recognised", b.Project, title)
			}
			other := "SW"
			if b.Project == "SW" {
				other = "RC"
			}
			if inboxHas(lines, other, title) {
				t.Errorf("%q matched under the wrong project code %s", title, other)
			}
		}
	}
	if n != 12 {
		t.Fatalf("expected 12 seed suggestions, saw %d", n)
	}
	// "Voice Morning Brief" is a real suggestion from the user's own LA briefing: it shares two
	// words with the "Morning push" line but is a different idea.
	for _, fresh := range []string{"Nightly digest of failed builds", "Dark mode for the lock screen", "Voice Morning Brief", "Autonomous Daily Study", "Bridge Challenge Queue"} {
		if inboxHas(lines, "LA", fresh) {
			t.Errorf("%q is new and must not be reported as already added", fresh)
		}
	}
	if inboxHas(lines, "LA", "Push") || inboxHas(lines, "LA", "a b") {
		t.Error("a single short word must not match on its own")
	}
}

func TestAddEndpointDoesNotDuplicateHandWrittenInboxLines(t *testing.T) {
	hn, md := briefHarness(t)
	writeBriefFile(t, md, sampleBuildList+"\n"+strings.Replace(realInboxLines, "LA suggestions", "LA suggestions", 1)+"\n")
	// the LA briefing in this harness has its own list, so use a title that the real line contains
	code, out := hn.req("POST", "/api/projectbrief/proj-la/md", "phone", map[string]any{"title": "Morning push", "why": "of the day's first three scheduled steps"})
	hn.mustOK(code, out, 200)
	if out["already"] != true {
		t.Fatalf("a hand-written inbox line was duplicated: %v", out)
	}
	if strings.Count(readStr(t, md), "Morning push") != 0 && strings.Contains(readStr(t, md), "lucille: LA suggestion") {
		t.Fatal("a second copy was written")
	}
}
