package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPlural(t *testing.T) {
	if got := plural(1, "is", "are"); got != "is" {
		t.Errorf("plural(1) = %q, want is", got)
	}
	if got := plural(2, "is", "are"); got != "are" {
		t.Errorf("plural(2) = %q, want are", got)
	}
	if got := plural(0, "is", "are"); got != "are" {
		t.Errorf("plural(0) = %q, want are", got)
	}
}

func TestLocalSystemPromptUnchangedWithNothingOpen(t *testing.T) {
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	oldList, oldBoard := masterBuildListPath, boardPath
	masterBuildListPath = filepath.Join(dir, "no-build-list.md")
	boardPath = filepath.Join(dir, "no-board.tsv")
	t.Cleanup(func() { masterBuildListPath, boardPath = oldList, oldBoard })

	if got := app.localSystemPrompt(); got != localPersona {
		t.Fatalf("with nothing open, want the bare persona, got %q", got)
	}
}

func TestLocalSystemPromptAddsCountsNotDetails(t *testing.T) {
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	oldList, oldBoard := masterBuildListPath, boardPath
	masterBuildListPath = writeBuildList(t, strings.Join([]string{
		"| SW-04 | P0 | Keys dead, a very specific secret detail nobody should see in the short prompt | TODO | |",
		"| RC-29 | P0 | lucille-core dead | TODO | |",
	}, "\n"))
	boardPath = writeBoard(t, time.Now().Add(-time.Minute).Format(time.RFC3339)+"\tdev-claude\tsome-area\tCLAIM\tworking on it right now")
	t.Cleanup(func() { masterBuildListPath, boardPath = oldList, oldBoard })

	got := app.localSystemPrompt()
	if !strings.HasPrefix(got, localPersona) {
		t.Fatalf("want the persona kept as a prefix, got %q", got)
	}
	if !strings.Contains(got, "2 critical") {
		t.Errorf("want the count '2 critical', got %q", got)
	}
	if !strings.Contains(got, "1 AI session") {
		t.Errorf("want the count '1 AI session', got %q", got)
	}
	if strings.Contains(got, "a very specific secret detail") {
		t.Error("the itemized build-list text must never reach the local (short) prompt")
	}
}
