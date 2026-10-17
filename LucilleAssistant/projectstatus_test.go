package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBuildList(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "MASTER_BUILD_LIST.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCriticalOpenItemsOnlyP0NotDone(t *testing.T) {
	old := masterBuildListPath
	t.Cleanup(func() { masterBuildListPath = old })
	masterBuildListPath = writeBuildList(t, strings.Join([]string{
		"| ID | P | Item | Status | Notes / source |",
		"|---|---|---|---|---|",
		"| SW-04 | P0 | Keys dead | TODO (user) | regenerate |",
		"| SW-11 | P2 | Apex IDE | WIP (other AI) | |",
		"| SW-27 | P2 | LUKS retired | DONE (decision) | |",
		"| RC-03 | P0 | Dr rules | DONE | written |",
		"| RC-29 | P0 | lucille-core dead | TODO | found 2026-10-14 |",
	}, "\n"))

	got := criticalOpenItems()
	if len(got) != 2 {
		t.Fatalf("want exactly 2 open P0 items (SW-04, RC-29), got %+v", got)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "SW-04") || !strings.Contains(joined, "RC-29") {
		t.Errorf("missing an expected P0 row: %+v", got)
	}
	if strings.Contains(joined, "SW-11") || strings.Contains(joined, "SW-27") || strings.Contains(joined, "RC-03") {
		t.Errorf("a P2 row or a DONE P0 row leaked through: %+v", got)
	}
}

func TestCriticalOpenItemsTruncatesLongText(t *testing.T) {
	old := masterBuildListPath
	t.Cleanup(func() { masterBuildListPath = old })
	long := strings.Repeat("x", 300)
	masterBuildListPath = writeBuildList(t, "| SW-99 | P0 | "+long+" | TODO | |\n")

	got := criticalOpenItems()
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	if !strings.HasSuffix(got[0], "...") || len([]rune(got[0])) > 180 {
		t.Errorf("want a truncated line ending in ..., got %d runes: %q", len([]rune(got[0])), got[0])
	}
}

func TestCriticalOpenItemsMissingFileReturnsNil(t *testing.T) {
	old := masterBuildListPath
	t.Cleanup(func() { masterBuildListPath = old })
	masterBuildListPath = filepath.Join(t.TempDir(), "does-not-exist.md")

	if got := criticalOpenItems(); got != nil {
		t.Errorf("want nil for a missing file, got %+v", got)
	}
}

func TestDispatchSummaryFlagsStale(t *testing.T) {
	old := boardPath
	t.Cleanup(func() { boardPath = old })
	boardPath = writeBoard(t, "2026-10-06T07:18:44-04:00\tswarm-claude\t/mnt/Lucy\tCLAIM\tISO build")

	got := dispatchSummary()
	if len(got) != 1 || !strings.Contains(got[0], "swarm-claude") || !strings.Contains(got[0], "STALE") {
		t.Fatalf("got %+v", got)
	}
}
