package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testActions never touches the real system: every command is a harmless coreutils call.
func testActions() []Action {
	return []Action{
		{ID: "say-hi", Label: "Say hi", Description: "Prints a line.", Command: []string{"/bin/echo", "hi there"}},
		{ID: "fail", Label: "Fails", Description: "Always exits 3.", Command: []string{"/bin/sh", "-c", "exit 3"}},
		{ID: "slow", Label: "Slow", Description: "Sleeps past its own timeout.", Command: []string{"/bin/sleep", "2"}, TimeoutSeconds: 1},
		{ID: "chatty", Label: "Chatty", Description: "Writes more than the output cap.", Command: []string{"/bin/sh", "-c", "yes x | head -c 200000"}},
		{ID: "leaky-env", Label: "Prints env", Description: "Would leak a key if the env were inherited.", Command: []string{"/usr/bin/env"}},
		// Two Sudo:true fixtures, for the sudoers-template test's count. This test environment has
		// no NOPASSWD rule installed (by design — that is the user's manual step), so a test that
		// actually ran one of these would see sudo correctly refuse; buildArgv's sudo-wrapping
		// itself is proven directly by TestBuildArgv instead.
		{ID: "drop-caches", Label: "Fake sudo action", Description: "Reclaim memory.", Command: []string{"/bin/true"}, Sudo: true},
		{ID: "power-off", Label: "Power off", Description: "Would power off the machine.", Command: []string{"/bin/true"}, Sudo: true, Destructive: true},
		// Destructive but not Sudo, so the confirm-gating test can assert on a real, successful run.
		{ID: "wipe-scratch", Label: "Wipe scratch", Description: "This would erase a scratch directory.", Command: []string{"/bin/true"}, Destructive: true},
	}
}

func TestNormalizeActionsRejectsBadEntries(t *testing.T) {
	in := []Action{
		{ID: "good-one", Command: []string{"/bin/true"}},
		{ID: "good-one", Command: []string{"/bin/false"}}, // duplicate id, dropped
		{ID: "Bad_ID", Command: []string{"/bin/true"}},    // uppercase/underscore not allowed
		{ID: "no-command"},                                            // empty command
		{ID: "relative", Command: []string{"true"}},                   // not an absolute path
		{ID: strings.Repeat("a", 41), Command: []string{"/bin/true"}}, // too long
	}
	out := normalizeActions(in)
	if len(out) != 1 || out[0].ID != "good-one" || out[0].Command[0] != "/bin/true" {
		t.Fatalf("normalizeActions = %+v, want only the first good-one", out)
	}
}

func TestDefaultActionsAreAllValid(t *testing.T) {
	d := defaultActions()
	if got := normalizeActions(d); len(got) != len(d) {
		t.Fatalf("a shipped default action failed validation: %d of %d survived", len(got), len(d))
	}
	seen := map[string]bool{}
	for _, a := range d {
		if seen[a.ID] {
			t.Errorf("duplicate default id %q", a.ID)
		}
		seen[a.ID] = true
		if a.Label == "" || a.Description == "" {
			t.Errorf("%s: missing label or description", a.ID)
		}
	}
}

func TestBuildArgv(t *testing.T) {
	plain := Action{Command: []string{"/bin/true", "x"}}
	if got := buildArgv(plain); len(got) != 2 || got[0] != "/bin/true" {
		t.Errorf("plain argv = %v", got)
	}
	sudo := Action{Command: []string{"/usr/bin/systemctl", "reboot"}, Sudo: true}
	got := buildArgv(sudo)
	want := []string{"/usr/bin/sudo", "-n", "--", "/usr/bin/systemctl", "reboot"}
	if len(got) != len(want) {
		t.Fatalf("sudo argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sudo argv = %v, want %v", got, want)
		}
	}
	// buildArgv must never alias the Action's own slice (a caller mutating the result
	// must not corrupt the registry for the next run).
	got[len(got)-1] = "CORRUPTED"
	if sudo.Command[len(sudo.Command)-1] == "CORRUPTED" {
		t.Fatal("buildArgv aliased the action's Command slice")
	}
}

func TestRunActionOutcomes(t *testing.T) {
	acts := map[string]Action{}
	for _, a := range testActions() {
		acts[a.ID] = a
	}
	ctx := context.Background()

	if r := runAction(ctx, acts["say-hi"]); !r.OK || r.ExitCode != 0 || !strings.Contains(r.Output, "hi there") {
		t.Fatalf("say-hi = %+v", r)
	}
	if r := runAction(ctx, acts["fail"]); r.OK || r.ExitCode != 3 {
		t.Fatalf("fail = %+v, want ok=false exit_code=3", r)
	}

	start := time.Now()
	r := runAction(ctx, acts["slow"])
	if !r.TimedOut || r.OK || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("slow = %+v (took %s), want a kill at ~1s", r, time.Since(start))
	}

	r = runAction(ctx, acts["chatty"])
	if !r.Truncated || len(r.Output) > maxActionOutput+1024 {
		t.Fatalf("chatty: truncated=%v len=%d, want truncated near the cap (%d)", r.Truncated, len(r.Output), maxActionOutput)
	}

	t.Setenv("OPENROUTER_API_KEY", "sk-leak-if-this-shows-up")
	r = runAction(ctx, acts["leaky-env"])
	if strings.Contains(r.Output, "sk-leak-if-this-shows-up") || strings.Contains(r.Output, "OPENROUTER") {
		t.Fatalf("the action's environment leaked a secret: %q", r.Output)
	}
	if !strings.Contains(r.Output, "PATH=") {
		t.Fatalf("env action produced no PATH at all: %q", r.Output)
	}
}

// ── HTTP ────────────────────────────────────────────────────────

func newTestAppWithActions(t *testing.T, acts []Action) *App {
	t.Helper()
	app, dir := setupTestApp(t)
	t.Cleanup(func() { os.RemoveAll(dir) })
	app.cfg.Actions = acts
	return app
}

// doActionsReq calls the handler directly (not through a ServeMux) so SetPathValue is not
// overwritten by the mux's own route matching — the same pattern briefings_test.go uses.
func doActionsReq(t *testing.T, app *App, method, id, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var r *http.Request
	var handler http.HandlerFunc
	if id == "" {
		r = httptest.NewRequest(method, "/api/actions", nil)
		handler = app.handleActions
	} else {
		var bodyReader *bytes.Reader
		if body != "" {
			bodyReader = bytes.NewReader([]byte(body))
			r = httptest.NewRequest(method, "/api/actions/x/run", bodyReader)
		} else {
			r = httptest.NewRequest(method, "/api/actions/x/run", nil)
			r.ContentLength = 0
		}
		r.SetPathValue("id", id)
		handler = app.handleActionRun
	}
	rr := httptest.NewRecorder()
	handler(rr, r)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	return rr, out
}

func TestHandleActionsListsWithoutRunning(t *testing.T) {
	app := newTestAppWithActions(t, testActions())
	rr, out := doActionsReq(t, app, "GET", "", "")
	if rr.Code != 200 {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	list, _ := out["actions"].([]any)
	if len(list) != len(testActions()) {
		t.Fatalf("got %d actions, want %d", len(list), len(testActions()))
	}
	first, _ := list[0].(map[string]any)
	if _, has := first["sudo"]; !has {
		t.Fatalf("action view missing a sudo field: %+v", first)
	}
}

func TestHandleActionRunHappyPath(t *testing.T) {
	app := newTestAppWithActions(t, testActions())
	rr, out := doActionsReq(t, app, "POST", "say-hi", "")
	if rr.Code != 200 || out["ok"] != true || !strings.Contains(out["output"].(string), "hi there") {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}

	// The audit log recorded it, with output truncated and no secrets expected.
	b, err := os.ReadFile(app.store.actionsLogPath())
	if err != nil || !strings.Contains(string(b), `"action_id":"say-hi"`) {
		t.Fatalf("audit log = %q, err=%v", string(b), err)
	}
}

func TestHandleActionRunUnknownID(t *testing.T) {
	app := newTestAppWithActions(t, testActions())
	rr, _ := doActionsReq(t, app, "POST", "no-such-action", "")
	if rr.Code != 404 {
		t.Fatalf("code=%d, want 404", rr.Code)
	}
	for _, bad := range []string{"../etc/passwd", "a/b", "UPPER", strings.Repeat("a", 60)} {
		if rr, _ := doActionsReq(t, app, "POST", bad, ""); rr.Code != 404 {
			t.Errorf("id %q: code=%d, want 404", bad, rr.Code)
		}
	}
}

func TestHandleActionRunDestructiveNeedsConfirm(t *testing.T) {
	app := newTestAppWithActions(t, testActions())
	rr, out := doActionsReq(t, app, "POST", "wipe-scratch", "")
	if rr.Code != 200 || out["ok"] == true || out["needs_confirm"] != true {
		t.Fatalf("without confirm: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if b, _ := os.ReadFile(app.store.actionsLogPath()); strings.Contains(string(b), "wipe-scratch") {
		t.Fatal("an unconfirmed destructive action must never be logged as run")
	}

	rr, out = doActionsReq(t, app, "POST", "wipe-scratch", `{"confirm": true}`)
	if rr.Code != 200 || out["ok"] != true {
		t.Fatalf("with confirm: code=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleActionRunIgnoresExtraBodyFields(t *testing.T) {
	// A phone (or anything else) cannot override what runs by stuffing other fields
	// into the body: only "confirm" is ever read.
	app := newTestAppWithActions(t, testActions())
	rr, out := doActionsReq(t, app, "POST", "say-hi", `{"confirm": true, "command": ["/bin/rm", "-rf", "/"], "id": "reboot"}`)
	if rr.Code != 200 || out["ok"] != true || !strings.Contains(out["output"].(string), "hi there") {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleActionRunSerializesConcurrentRuns(t *testing.T) {
	app := newTestAppWithActions(t, testActions())
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rr, _ := doActionsReq(t, app, "POST", "slow", "")
			codes[i] = rr.Code
		}(i)
	}
	wg.Wait()
	var ok200, got409 int
	for _, c := range codes {
		if c == 200 {
			ok200++
		} else if c == 409 {
			got409++
		}
	}
	if ok200 == 0 || got409 == 0 {
		t.Fatalf("codes = %v, want a mix of 200 (ran) and 409 (busy)", codes)
	}
}

func TestToActionViewShowsSudoPrefix(t *testing.T) {
	v := toActionView(Action{Command: []string{"/usr/bin/systemctl", "reboot"}, Sudo: true})
	if v.Command != "sudo /usr/bin/systemctl reboot" {
		t.Fatalf("command display = %q", v.Command)
	}
}

func TestSudoersTemplateListsOnlySudoActions(t *testing.T) {
	var sb strings.Builder
	if err := runSudoersTemplate(testActions(), &sb); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	if strings.Count(out, "NOPASSWD:") != 2 { // drop-caches and reboot are Sudo:true
		t.Fatalf("template =\n%s", out)
	}
	if !strings.Contains(out, "NOPASSWD: /bin/true") {
		t.Fatalf("template is missing the expected command:\n%s", out)
	}
	if strings.Contains(out, "say-hi") || strings.Contains(out, "echo") {
		t.Fatalf("a non-sudo action leaked into the sudoers template:\n%s", out)
	}
}

func TestActionsCLIListAndSudoersTemplate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LUCILLE_ASSISTANT_CONFIG", filepath.Join(home, "config.json"))
	t.Setenv("LUCILLE_ASSISTANT_DATA", filepath.Join(home, "data"))

	var sb strings.Builder
	if rc := runActionsCLI([]string{"list"}, &sb); rc != 0 {
		t.Fatalf("list rc=%d out=%q", rc, sb.String())
	}
	if !strings.Contains(sb.String(), "check-mounts") {
		t.Fatalf("list output missing a default action:\n%s", sb.String())
	}

	sb.Reset()
	if rc := runActionsCLI([]string{"sudoers-template"}, &sb); rc != 0 {
		t.Fatalf("sudoers-template rc=%d out=%q", rc, sb.String())
	}
	if n := strings.Count(sb.String(), "NOPASSWD:"); n == 0 {
		t.Fatalf("sudoers-template produced no NOPASSWD lines:\n%s", sb.String())
	}

	sb.Reset()
	if rc := runActionsCLI([]string{"bogus"}, &sb); rc != 2 {
		t.Fatalf("unknown subcommand rc=%d, want 2", rc)
	}
}
