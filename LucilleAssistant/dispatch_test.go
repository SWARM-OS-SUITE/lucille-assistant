package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeBoard(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ai-board.tsv")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseActiveClaimsClosesOnDone(t *testing.T) {
	path := writeBoard(t,
		"2026-10-14T10:00:00-04:00\tdev-claude\tweb/index.html\tCLAIM\tretheme",
		"2026-10-14T10:05:00-04:00\tdev-claude\tweb/index.html\tDONE\tdone retheming",
	)
	got, err := parseActiveClaims(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want no active claims after a DONE, got %+v", got)
	}
}

func TestParseActiveClaimsLeavesOpenClaimActive(t *testing.T) {
	path := writeBoard(t,
		"2026-10-14T10:00:00-04:00\tswarm-claude\t/mnt/Lucy\tCLAIM\tISO build",
	)
	got, err := parseActiveClaims(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AI != "swarm-claude" || got[0].Area != "/mnt/Lucy" {
		t.Fatalf("got %+v", got)
	}
}

// The bug this guards: an old abandoned claim (swarm-claude's real /mnt/Lucy claim from
// 2026-10-06, never closed) must not be reported as confidently active — it must be flagged
// stale so nobody trusts it the way the user was burned once already.
func TestOldUnclosedClaimIsFlaggedStale(t *testing.T) {
	old := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	path := writeBoard(t, old+"\tswarm-claude\t/mnt/Lucy\tCLAIM\tISO build running")
	got, err := parseActiveClaims(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Stale {
		t.Fatalf("want one stale claim, got %+v", got)
	}
}

func TestRecentClaimIsNotStale(t *testing.T) {
	recent := time.Now().Add(-10 * time.Minute).Format(time.RFC3339)
	path := writeBoard(t, recent+"\tdev-claude\tsome-area\tCLAIM\tworking")
	got, err := parseActiveClaims(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Stale {
		t.Fatalf("want one fresh, non-stale claim, got %+v", got)
	}
}

func TestMultipleAreasAndReclaimAfterDone(t *testing.T) {
	now := time.Now()
	fmt := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	path := writeBoard(t,
		fmt(-30*time.Minute)+"\tdev-claude\tareaA\tCLAIM\tfirst pass",
		fmt(-20*time.Minute)+"\tdev-claude\tareaA\tDONE\tfinished first pass",
		fmt(-10*time.Minute)+"\tdev-claude\tareaA\tCLAIM\tsecond pass",
		fmt(-30*time.Minute)+"\tclaude-recovery\tareaB\tCLAIM\tother work",
	)
	got, err := parseActiveClaims(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 active claims (areaA re-claimed, areaB open), got %+v", got)
	}
	byArea := map[string]boardClaim{}
	for _, c := range got {
		byArea[c.Area] = c
	}
	if byArea["areaA"].Task != "second pass" {
		t.Errorf("areaA should reflect the re-claim, got %+v", byArea["areaA"])
	}
	if byArea["areaB"].AI != "claude-recovery" {
		t.Errorf("areaB missing: %+v", byArea)
	}
}

func TestMissingBoardFileReturnsError(t *testing.T) {
	if _, err := parseActiveClaims("/nonexistent/path/ai-board.tsv"); err == nil {
		t.Fatal("want an error for a missing board file")
	}
}

func TestMalformedLinesAreSkipped(t *testing.T) {
	path := writeBoard(t,
		"not a valid line at all",
		"2026-10-14T10:00:00-04:00\tdev-claude\tareaC",
		"2026-10-14T10:00:00-04:00\tdev-claude\tareaD\tCLAIM\tok",
	)
	got, err := parseActiveClaims(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Area != "areaD" {
		t.Fatalf("want only the well-formed row, got %+v", got)
	}
}
