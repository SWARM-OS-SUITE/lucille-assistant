package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeClipboard swaps the real desktop clipboard for a variable for the length of one test.
func fakeClipboard(t *testing.T, fail error) *string {
	t.Helper()
	var got string
	old := setPCClipboard
	setPCClipboard = func(_ context.Context, text string) (string, error) {
		if fail != nil {
			return "", fail
		}
		got = text
		return "fake", nil
	}
	t.Cleanup(func() { setPCClipboard = old })
	t.Setenv("LUCILLE_CLIPBOARD", "")
	t.Setenv("HOME", t.TempDir()) // no clipboard.disabled from the real config dir
	return &got
}

func TestClipboardPutsTextOnThePC(t *testing.T) {
	got := fakeClipboard(t, nil)
	a := threadsApp(t)
	w := postJSON(a.handleClipboard, "/api/clipboard", map[string]string{"text": "claude -p 'fix the deals list'"}, &Caller{Kind: callerPhone})
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if *got != "claude -p 'fix the deals list'" {
		t.Fatalf("clipboard got %q", *got)
	}
	var out struct {
		Via      string `json:"via"`
		Redacted int    `json:"redacted"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Via != "fake" || out.Redacted != 0 {
		t.Fatalf("reply %s", w.Body)
	}
}

func TestClipboardBlanksKeys(t *testing.T) {
	got := fakeClipboard(t, nil)
	a := threadsApp(t)
	w := postJSON(a.handleClipboard, "/api/clipboard", map[string]string{"text": "export OPENROUTER_API_KEY=sk-or-v1-abcdefghijklmnopqrstuvwxyz0123"}, nil)
	if w.Code != 200 {
		t.Fatalf("status %d %s", w.Code, w.Body)
	}
	if strings.Contains(*got, "abcdefghijklmnop") || !strings.Contains(*got, "[redacted]") {
		t.Fatalf("key reached the clipboard: %q", *got)
	}
	if !strings.Contains(w.Body.String(), `"redacted":1`) {
		t.Fatalf("reply should count the blanked part: %s", w.Body)
	}
}

func TestClipboardRules(t *testing.T) {
	got := fakeClipboard(t, nil)
	a := threadsApp(t)
	cases := []struct {
		name string
		text string
		c    *Caller
		code int
	}{
		{"empty", "   ", nil, 400},
		{"too long", strings.Repeat("a", clipboardMax+1), nil, 413},
		{"nul byte", "a\x00b", nil, 400},
		{"agent", "hello", &Caller{Kind: callerAgent}, 403},
		{"bad token", "hello", &Caller{Kind: callerPhone, BadToken: true}, 401},
	}
	for _, c := range cases {
		w := postJSON(a.handleClipboard, "/api/clipboard", map[string]string{"text": c.text}, c.c)
		if w.Code != c.code {
			t.Errorf("%s: status %d, want %d (%s)", c.name, w.Code, c.code, w.Body)
		}
	}
	if *got != "" {
		t.Fatalf("a refused request reached the clipboard: %q", *got)
	}
}

func TestClipboardKillSwitchAndFailure(t *testing.T) {
	fakeClipboard(t, nil)
	a := threadsApp(t)
	t.Setenv("LUCILLE_CLIPBOARD", "0")
	if w := postJSON(a.handleClipboard, "/api/clipboard", map[string]string{"text": "x"}, nil); w.Code != 403 {
		t.Fatalf("kill switch: %d", w.Code)
	}

	fakeClipboard(t, errors.New("the PC's clipboard did not accept it"))
	w := postJSON(a.handleClipboard, "/api/clipboard", map[string]string{"text": "x"}, nil)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "did not accept") {
		t.Fatalf("failure: %d %s", w.Code, w.Body)
	}
}
