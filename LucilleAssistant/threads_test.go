package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func threadsApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("LUCILLE_THREADS_FILE", "")
	return &App{cfg: Config{DataDir: t.TempDir()}}
}

func postJSON(h http.HandlerFunc, path string, body any, c *Caller) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(b))
	if c != nil {
		r = withCaller(r, *c)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestThreadPostAndRead(t *testing.T) {
	a := threadsApp(t)
	w := postJSON(a.handleThreadPost, "/api/threads/post", map[string]string{"anchor": "item:MESH-02", "text": "pair the nodes", "quote": "zero nodes paired"}, nil)
	if w.Code != 200 {
		t.Fatalf("post: %d %s", w.Code, w.Body)
	}
	msgs := a.readThreads()
	if len(msgs) != 1 || msgs[0].Anchor != "item:MESH-02" || msgs[0].By != "you" || msgs[0].Quote != "zero nodes paired" {
		t.Fatalf("stored: %+v", msgs)
	}
	if !strings.Contains(a.threadDigest(), "zero nodes paired") {
		t.Fatalf("digest should carry the quote: %q", a.threadDigest())
	}
}

func TestThreadPostRules(t *testing.T) {
	a := threadsApp(t)
	agent := Caller{Kind: callerAgent}
	if w := postJSON(a.handleThreadPost, "/x", map[string]string{"anchor": "item:A-1", "text": "hi"}, &agent); w.Code != http.StatusForbidden {
		t.Fatalf("Dr. Boltz's agent token must not post: %d", w.Code)
	}
	for _, bad := range []string{"", "nope", "item:", "../etc", "item:a b", "x:y z"} {
		if w := postJSON(a.handleThreadPost, "/x", map[string]string{"anchor": bad, "text": "hi"}, nil); w.Code != 400 {
			t.Errorf("anchor %q accepted: %d", bad, w.Code)
		}
	}
	if w := postJSON(a.handleThreadAsk, "/x", map[string]string{"anchor": "item:A-1", "who": "unknown"}, nil); w.Code != 400 {
		t.Fatalf("an invalid who must be rejected: %d", w.Code)
	}
	if w := postJSON(a.handleThreadAsk, "/x", map[string]string{"anchor": "item:A-1", "who": "claude"}, nil); w.Code != 200 {
		t.Fatalf("claude should be accepted: %d", w.Code)
	}
	if w := postJSON(a.handleThreadAsk, "/x", map[string]string{"anchor": "item:A-1", "who": "auto"}, nil); w.Code != 200 {
		t.Fatalf("auto should be accepted: %d", w.Code)
	}
}

func TestThreadDeal(t *testing.T) {
	a := threadsApp(t)
	// Deal creates the comment, job, and Lucille's confirmation in the thread
	w := postJSON(a.handleThreadDeal, "/api/threads/deal", map[string]any{
		"anchor": "item:SW-01",
		"text":   "deal with this now",
		"quote":  "SW-01: fix boot",
		"to":     "auto",
	}, nil)
	if w.Code != 200 {
		t.Fatalf("deal failed: %d %s", w.Code, w.Body)
	}
	var resp struct {
		OK       bool
		Target   string
		Launched bool
		Job      Job
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Job.ID == "" || resp.Target == "" {
		t.Fatalf("unexpected deal response: %+v", resp)
	}
	msgs := a.readThreads()
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages (human comment + lucille job notice), got %d", len(msgs))
	}
	if msgs[0].By != "you" || msgs[0].Text != "deal with this now" {
		t.Fatalf("expected first message to be human comment, got %+v", msgs[0])
	}
	if msgs[1].By != "lucille" || msgs[1].Kind != "job" {
		t.Fatalf("expected second message to be lucille job notice, got %+v", msgs[1])
	}
}

// Append-only means a merge from another machine is a union by id: duplicates and torn lines are skipped.
func TestThreadFileMergesByID(t *testing.T) {
	a := threadsApp(t)
	m1, _ := a.appendThread(ThreadMsg{Anchor: "ai:codex", By: "you", Text: "one"})
	b, _ := os.ReadFile(a.threadsPath())
	other := `{"id":"t-other","anchor":"ai:codex","at":"2099-01-01T00:00:00Z","by":"boltz","text":"from recovery os"}` + "\n"
	torn := `{"id":"t-torn","anch` + "\n"
	if err := os.WriteFile(a.threadsPath(), append(append(b, b...), []byte(other+torn)...), 0o600); err != nil {
		t.Fatal(err)
	}
	got := a.readThreads()
	if len(got) != 2 || got[0].ID != m1.ID || got[1].ID != "t-other" {
		t.Fatalf("merge: %+v", got)
	}
}

func TestSuggestionThreadKeepsSchedule(t *testing.T) {
	a := threadsApp(t)
	if w := postJSON(a.handleThreadPost, "/x", map[string]string{"anchor": "sg-0a1b2c3d", "text": "done", "title": "Boot test"}, nil); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	cf, _ := a.readComments()
	if it := cf.Items["sg-0a1b2c3d"]; it == nil || it.Status != planDone {
		t.Fatalf("a suggestion thread should still schedule: %+v", it)
	}
	// non-suggestion anchors are conversation only: the words must not touch the schedule
	postJSON(a.handleThreadPost, "/x", map[string]string{"anchor": "item:MESH-02", "text": "done"}, nil)
	cf, _ = a.readComments()
	if cf.Items["item:MESH-02"] != nil {
		t.Fatal("a comment on a build-list item must not create a schedule entry")
	}
}

func TestStripReplyDecorations(t *testing.T) {
	if got := stripThink("<think>hmm</think>[DR.BOLTZ] Use /dev/sdb2."); got != "Use /dev/sdb2." {
		t.Fatalf("got %q", got)
	}
	if got := plain("Run `a.sh` to a **non-build** partition"); got != "Run a.sh to a non-build partition" {
		t.Fatalf("got %q", got)
	}
}

func TestRememberDetection(t *testing.T) {
	yes := map[string]string{
		"remember that the backup goes to the T7":   "the backup goes to the T7",
		"Idea: widget on the home screen":           "widget on the home screen",
		"Lucille, please remember: boltz is memory": "boltz is memory",
		"change the brief to show hours":            "the brief to show hours",
		"add to the list a boot test":               "a boot test",
	}
	for in, want := range yes {
		if got, ok := rememberText(in); !ok || got != want {
			t.Errorf("%q -> %q %v, want %q", in, got, ok, want)
		}
	}
	for _, no := range []string{"how are you", "I remember when we met", "can you change it", "notes are fine", "remember"} {
		if _, ok := rememberText(no); ok {
			t.Errorf("%q must not trigger a save", no)
		}
	}
}

func TestJobMailboxRoundTrip(t *testing.T) {
	a := threadsApp(t)
	w := postJSON(a.handleJobCreate, "/api/jobs", map[string]string{"anchor": "item:SW-01", "to": "claude", "prompt": "do it; report to /api/jobs/{{JOB_ID}}/done"}, nil)
	if w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created struct{ Job Job }
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id := created.Job.ID
	if strings.Contains(created.Job.Prompt, "{{JOB_ID}}") || !strings.Contains(created.Job.Prompt, id) {
		t.Fatalf("the job id must be filled into the prompt: %q", created.Job.Prompt)
	}
	if got := a.queuedFor("dev-claude"); len(got) != 1 {
		t.Fatalf("a claude session should see it: %+v", got)
	}
	if got := a.queuedFor("dev-antigravity"); len(got) != 0 {
		t.Fatalf("antigravity must not see claude's mail: %+v", got)
	}
	mk := func(h http.HandlerFunc, body map[string]string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/x", bytes.NewReader(b))
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	if w := mk(a.handleJobClaim, map[string]string{"ai": "dev-antigravity"}); w.Code == 200 {
		t.Fatal("the wrong AI must not be able to claim it")
	}
	if w := mk(a.handleJobClaim, map[string]string{"ai": "dev-claude"}); w.Code != 200 {
		t.Fatalf("claim: %d %s", w.Code, w.Body)
	}
	if got := a.queuedFor("dev-claude"); len(got) != 0 {
		t.Fatal("a claimed job leaves the mailbox")
	}
	if w := mk(a.handleJobDone, map[string]string{"ai": "lucille", "summary": "fake"}); w.Code != 400 {
		t.Fatalf("a coder report must not be able to pose as Lucille: %d", w.Code)
	}
	if w := mk(a.handleJobDone, map[string]string{"ai": "dev-claude", "summary": "built it, tests pass"}); w.Code != 200 {
		t.Fatalf("done: %d %s", w.Code, w.Body)
	}
	msgs := a.readThreads()
	last := msgs[len(msgs)-1]
	if last.By != "dev-claude" || last.Kind != "coder" || !strings.Contains(last.Text, "tests pass") {
		t.Fatalf("the report must land in the thread: %+v", last)
	}
}
