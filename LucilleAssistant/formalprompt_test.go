package main

import (
	"strings"
	"testing"
)

func TestFormalPromptShape(t *testing.T) {
	p := formalPrompt{
		Who: "Lucille, for the user.", Job: "Fix the deals list.\nIt repeats games.", Background: "Game Mode shows deals.",
		Said: []string{"you: it shows Hades forever", " "}, ReadFirst: []string{"/a/README.md"},
		Project: "LA", Dir: "/mnt/x", Rules: []string{"one", "two"},
		DoneWhen: "No repeats.", Report: "Say what changed.",
	}
	text := p.Text()
	want := []string{"WHO:", "THE JOB:", "BACKGROUND:", "WHAT HAS BEEN SAID:", "READ FIRST:", "WHERE:", "HOW TO WORK:", "DONE WHEN:", "REPORT BACK:"}
	last := -1
	for _, h := range want {
		i := strings.Index(text, h)
		if i < 0 || i < last {
			t.Fatalf("heading %q missing or out of order in:\n%s", h, text)
		}
		last = i
	}
	if !strings.Contains(text, "1. one\n2. two") || !strings.Contains(text, "- you: it shows Hades forever") {
		t.Fatalf("rules or bullets wrong:\n%s", text)
	}
	if strings.Count(text, "\n- ") != 2 { // the blank "said" line is dropped
		t.Fatalf("blank bullet kept:\n%s", text)
	}

	line := p.OneLine(2000)
	if strings.ContainsAny(line, "\n\r") || !strings.HasPrefix(line, "WHO: ") || !strings.Contains(line, " | REPORT BACK: Say what changed.") {
		t.Fatalf("one-line form wrong: %q", line)
	}

	// Empty sections are left out, not printed as bare headings.
	if got := (formalPrompt{Job: "x"}).Text(); got != "THE JOB:\nx\n" {
		t.Fatalf("empty sections printed: %q", got)
	}
}

func TestDraftPromptIsFormal(t *testing.T) {
	a := threadsApp(t)
	got := a.draftPrompt("item:SW-01", "claude")
	for _, h := range []string{"WHO:", "THE JOB:", "HOW TO WORK:", "DONE WHEN:", "REPORT BACK:", "{{JOB_ID}}", "AI_INSTRUCTIONS.md", "Dr. Boltz"} {
		if !strings.Contains(got, h) {
			t.Fatalf("draft is missing %q:\n%s", h, got)
		}
	}
}
