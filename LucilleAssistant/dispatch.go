package main

import (
	"bufio"
	"net/http"
	"os"
	"strings"
	"time"
)

// Dispatch: "who's doing what, what's free" for the multi-AI board, read straight from the real
// ai-board.tsv (append-only: <RFC3339 time> <ai> <area> <CLAIM|DONE> <note>, tab-separated) that
// every AI session on this PC already writes to via ai-board.sh. No separate state of our own —
// this is a read-only view of the one real board, so it can never go stale on its own account.
//
// An area counts as actively claimed when its last CLAIM for a given (ai, area) pair has no later
// DONE for that exact same pair. A claim older than claimStaleAfter with no DONE is still reported, but
// flagged stale: the user has already been burned once by trusting an old claim nobody closed out
// (swarm-claude's /mnt/Lucy claim from 2026-10-06, abandoned, never marked done).

var boardPath = "/mnt/recoverybuild/00-CURRENT/ai-board.tsv"

const claimStaleAfter = 6 * time.Hour

type boardClaim struct {
	AI        string    `json:"ai"`
	Area      string    `json:"area"`
	Task      string    `json:"task"`
	ClaimedAt time.Time `json:"claimed_at"`
	Stale     bool      `json:"stale"`
}

// parseActiveClaims walks the board in file order (oldest first) and keeps, per (ai, area) pair,
// only whether its most recent row is a CLAIM (active) or a DONE (closed).
func parseActiveClaims(path string) ([]boardClaim, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	type pending struct {
		task string
		at   time.Time
	}
	open := map[string]pending{} // "ai\x00area" -> still-open claim
	seen := map[string]bool{}    // every key ever seen, so a re-claim after a DONE isn't added twice
	order := []string{}          // first-seen order, for stable output

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 4 {
			continue
		}
		ts, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}
		ai, area, action := strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2]), strings.TrimSpace(parts[3])
		note := ""
		if len(parts) == 5 {
			note = strings.TrimSpace(parts[4])
		}
		key := ai + "\x00" + area
		switch action {
		case "CLAIM":
			if !seen[key] {
				seen[key] = true
				order = append(order, key)
			}
			open[key] = pending{task: note, at: ts}
		case "DONE":
			delete(open, key)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	now := time.Now()
	out := make([]boardClaim, 0, len(open))
	for _, key := range order {
		p, ok := open[key]
		if !ok {
			continue // closed by a later DONE
		}
		ai, area, _ := strings.Cut(key, "\x00")
		out = append(out, boardClaim{
			AI: ai, Area: area, Task: p.task, ClaimedAt: p.at,
			Stale: now.Sub(p.at) > claimStaleAfter,
		})
	}
	return out, nil
}

// handleDispatch answers who is actively working where, straight from the real board. Always
// ok:true; an empty list with a reason says why (board not mounted/readable here).
func (a *App) handleDispatch(w http.ResponseWriter, r *http.Request) {
	claims, err := parseActiveClaims(boardPath)
	if err != nil {
		writeJSON(w, map[string]any{"ok": true, "active": []boardClaim{}, "reason": "unavailable"})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "active": claims, "generated_at": time.Now()})
}
