package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// "Approve and start" on a brief suggestion (user 2026-10-15): the suggestion's anchor must lead the
// launcher to its own project's directory, spelled the way it exists on this boot.

func firstSuggestion(t *testing.T, a *App, project string) BriefSuggestion {
	t.Helper()
	projects, _ := a.buildBrief(time.Now())["projects"].([]BriefProject)
	for _, p := range projects {
		if p.ID == project && len(p.Suggestions) > 0 {
			return p.Suggestions[0]
		}
	}
	t.Fatalf("no suggestions for %s", project)
	return BriefSuggestion{}
}

func TestSuggestionAnchorFindsItsProjectDir(t *testing.T) {
	hn, _ := briefHarness(t)
	a := hn.app
	sg := firstSuggestion(t, a, "proj-la")
	if !anchorPattern.MatchString(sg.ID) {
		t.Fatalf("suggestion id %q is not a valid anchor", sg.ID)
	}
	code, dir := a.projectDirFor(sg.ID)
	if code != "LA" || dir != a.cfg.Brief.Dirs["proj-la"] {
		t.Fatalf("projectDirFor(%s) = %q %q", sg.ID, code, dir)
	}
	if _, dir := a.projectDirFor("sg-00000000"); dir != "" {
		t.Fatalf("an unknown suggestion must not get a directory, got %q", dir)
	}
}

func TestProjectDirUsesTheLiveSpelling(t *testing.T) {
	hn, _ := briefHarness(t)
	a := hn.app
	live := t.TempDir()
	if err := os.MkdirAll(filepath.Join(live, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Configured under a mount point that is not there on this boot; its alias is.
	a.cfg.Brief.Dirs["proj-la"] = "/nonexistent-lucillebuild/src"
	a.cfg.Brief.Aliases = map[string]string{"/nonexistent-lucillebuild": live}
	if _, dir := a.projectDirFor("proj:proj-la"); dir != filepath.Join(live, "src") {
		t.Fatalf("launcher would chdir into a missing directory: %q", dir)
	}
}

func TestSuggestionDraftIsItsFormalPrompt(t *testing.T) {
	hn, _ := briefHarness(t)
	a := hn.app
	t.Setenv("LUCILLE_THREADS_FILE", "")
	sg := firstSuggestion(t, a, "proj-la")
	got := a.draftPrompt(sg.ID, "claude")
	for _, want := range []string{"WHO:", "THE JOB:", sg.Title, "READ FIRST:", "WHERE:", a.cfg.Brief.Dirs["proj-la"], "HOW TO WORK:", "Dr. Boltz", "{{JOB_ID}}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("draft is missing %q:\n%s", want, got)
		}
	}
}

func TestApproveAndStartRules(t *testing.T) {
	hn, _ := briefHarness(t)
	a := hn.app
	t.Setenv("LUCILLE_THREADS_FILE", "")
	t.Setenv("HOME", t.TempDir()) // no launch.disabled from the real config dir
	sg := firstSuggestion(t, a, "proj-la")
	phone := &Caller{Kind: callerPhone}

	// Never actually start a coder in a test: switched off, the request is refused before anything runs.
	t.Setenv("LUCILLE_LAUNCH", "0")
	w := postJSON(a.handleJobCreate, "/api/jobs", map[string]any{"anchor": sg.ID, "to": "claude", "prompt": "do it", "launch": true}, phone)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "switched off") {
		t.Fatalf("kill switch: %d %s", w.Code, w.Body)
	}
	t.Setenv("LUCILLE_LAUNCH", "")
	w = postJSON(a.handleJobCreate, "/api/jobs", map[string]any{"anchor": "sg-00000000", "to": "claude", "prompt": "do it", "launch": true}, phone)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "which project") {
		t.Fatalf("unknown suggestion: %d %s", w.Code, w.Body)
	}
	w = postJSON(a.handleJobCreate, "/api/jobs", map[string]any{"anchor": sg.ID, "to": "claude", "prompt": "do it", "launch": true}, &Caller{Kind: callerAgent})
	if w.Code != 403 {
		t.Fatalf("Dr. Boltz's agent may not approve a launch: %d", w.Code)
	}
	// Without launch it still goes to the mailbox, anchored on the suggestion.
	w = postJSON(a.handleJobCreate, "/api/jobs", map[string]any{"anchor": sg.ID, "to": "claude", "prompt": "do it"}, phone)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"anchor":"`+sg.ID+`"`) {
		t.Fatalf("mailbox: %d %s", w.Code, w.Body)
	}
}
