package main

import (
	"os"
	"regexp"
	"strings"
)

// Live project status, fed into the chat system prompt (see systemPrompt in briefings.go) so the
// user can ask Lucille things like "what's blocking SWARM OS" or "who's working on what" and get
// a real answer sourced from the one shared build list and the shared ai-board — not a guess.

var masterBuildListPath = "/mnt/recoverybuild/00-CURRENT/MASTER_BUILD_LIST.md"

// buildListRowRe matches a build-list table row: "| ID | Pn | item text | status | notes |".
// Cell content in this file never contains a literal "|", so splitting on it is safe.
var buildListRowRe = regexp.MustCompile(`^\|\s*([A-Za-z]+-\d+)\s*\|\s*(P\d)[^|]*\|\s*(.*?)\s*\|\s*([^|]*?)\s*\|`)

// criticalOpenItems reads the one build list and returns every P0 row not marked DONE — condensed
// on purpose: the full file is ~280 lines, far more than belongs in every chat turn. Only the
// handful of things that would actually block someone goes to the model.
func criticalOpenItems() []string {
	b, err := os.ReadFile(masterBuildListPath)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		m := buildListRowRe.FindStringSubmatch(line)
		if m == nil || m[2] != "P0" {
			continue
		}
		status := m[4]
		if strings.HasPrefix(status, "DONE") {
			continue
		}
		item := m[3]
		if r := []rune(item); len(r) > 160 {
			item = string(r[:160]) + "..."
		}
		out = append(out, m[1]+" ("+status+"): "+item)
	}
	return out
}

// dispatchSummary is the live ai-board state, condensed: who's actively on what, with stale
// (likely-abandoned) claims called out so Lucille never repeats one as if it were real work in
// progress — the user was burned once already by an old unclosed claim.
func dispatchSummary() []string {
	claims, err := parseActiveClaims(boardPath)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		tag := "active"
		if c.Stale {
			tag = "STALE, likely abandoned"
		}
		out = append(out, c.AI+" on "+c.Area+" ("+tag+")")
	}
	return out
}
