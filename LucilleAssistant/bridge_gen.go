package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Lucille's two jobs in the channel: thinking up challenges for Dr. Boltz, and answering
// when someone talks to her.

const challengeSystem = `You are Lucille, the planner in a two-AI engineering loop. Dr. Boltz is an AI coder who implements your challenges in a throw-away git branch; a person reviews every result before anything merges.
Write challenges that are:
- small enough for one 20-minute coding session,
- objectively checkable (a test, a build, a grep, a command with a known output),
- self-contained: no network, no secrets, no root, no installing packages, nothing outside the named repo,
- useful to the repo's owner, not busywork.
Put the acceptance criteria in the body. Reply with ONLY a JSON array of objects {"title","body","repo","size"}, where repo is one of the repo names you are given and size is evening, weekend or sprint. No prose.`

const bridgeChatSystem = `You are Lucille in a shared engineering channel with Dr. Boltz (an AI coder) and the user. Answer in at most three short sentences of plain text, no markdown. You can suggest challenges, but you cannot run code, approve work or touch the machine. Never repeat secrets or tokens.`

type genCandidate struct{ Title, Body, Repo, Size string }

// curatedChallenges are used when no language model answers. They are real, checkable
// jobs taken from this project's own notes, so the loop is useful even offline.
var curatedChallenges = []genCandidate{
	{
		Title: "Fix the lucille-android Void color compile error",
		Repo:  "lucille",
		Size:  "evening",
		Body: "lucille-android/ does not compile: MainActivity.kt (around lines 79 and 98) and SetupScreen.kt (around line 28) use a color named Void that ui/Theme.kt no longer defines. Theme.kt has VoidDeep, Void2 and VoidPanel instead (broken by commit 6d86c56).\n" +
			"Replace each use of the undefined Void with the closest existing color so the look stays the same.\n" +
			"Acceptance: grep finds no reference to an undefined Void color anywhere under lucille-android/, and go test ./... still passes. Do not run Gradle.",
	},
	{
		Title: "Test that every powerful bridge and action route is cross-site guarded",
		Repo:  "lucille",
		Size:  "evening",
		Body: "main.go registers routes on a mux. Add a Go test that reads main.go and fails if any route under /api/bridge, /api/actions or /api/pair/new is registered without being wrapped in browserSafe(...).\n" +
			"Acceptance: the test passes on the current code, and temporarily removing browserSafe from one of those routes makes it fail.",
	},
}

// generateChallenges asks the language model for up to n challenges and queues them as
// Lucille's. Context given to the model is repo metadata and the titles already queued,
// nothing from mail, news or the web: what Lucille writes here is executed by an
// autonomous coder, so untrusted text must not be able to steer it.
func (a *App) generateChallenges(ctx context.Context, n int, repoFilter string) ([]Challenge, string, error) {
	if n < 1 {
		n = 3
	}
	if n > 5 {
		n = 5
	}
	b := a.bridge
	b.mu.Lock()
	repos := append([]BridgeRepo(nil), b.cfg.Repos...)
	var existing []string
	for i := len(b.chal) - 1; i >= 0 && len(existing) < 40; i-- {
		existing = append(existing, b.chal[i].Title)
	}
	b.mu.Unlock()
	if repoFilter != "" {
		var keep []BridgeRepo
		for _, r := range repos {
			if r.Name == repoFilter {
				keep = append(keep, r)
			}
		}
		if len(keep) == 0 {
			return nil, "", fmt.Errorf("unknown repo %q", repoFilter)
		}
		repos = keep
	}
	if len(repos) == 0 {
		return nil, "", fmt.Errorf("no repos are configured for the bridge")
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Write %d challenges. Repos:\n", n)
	for _, r := range repos {
		fmt.Fprintf(&sb, "- %s: %s\n", r.Name, r.Desc)
		if rs := repoStatus(r.Path); rs.Error == "" {
			fmt.Fprintf(&sb, "  branch %s, %d uncommitted changes, last commit %q\n", rs.Branch, rs.Dirty+rs.Untracked, cleanText(rs.LastCommit, 100))
		}
		if log, err := git(r.Path, "log", "-8", "--format=%s"); err == nil {
			fmt.Fprintf(&sb, "  recent commits: %s\n", cleanText(strings.ReplaceAll(log, "\n", " | "), 600))
		}
	}
	if len(existing) > 0 {
		fmt.Fprintf(&sb, "Already queued or done (do not repeat): %s\n", cleanText(strings.Join(existing, " | "), 1500))
	}

	source := "model"
	text, err := a.ask(ctx, challengeSystem, sb.String(), 1600)
	cands, perr := parseCandidates(text)
	if err != nil || perr != nil || len(cands) == 0 {
		source = "curated"
		cands = nil
		for _, c := range curatedChallenges {
			if repoFilter == "" || c.Repo == repoFilter {
				cands = append(cands, c)
			}
		}
	}

	made := []Challenge{}
	for _, c := range cands {
		if len(made) >= n {
			break
		}
		ch, err := b.addChallenge(c.Title, c.Body, strings.TrimSpace(c.Repo), "lucille", c.Size)
		if err != nil {
			continue // duplicate, unknown repo, or the queue is full
		}
		made = append(made, ch)
	}
	if len(made) > 0 {
		b.emit(BridgeEvent{Who: "lucille", To: "boltz", Kind: "say",
			Text: fmt.Sprintf("I queued %d challenge(s) for you, Dr. Boltz (%s). Take them in order.", len(made), source)})
	}
	return made, source, nil
}

func parseCandidates(text string) ([]genCandidate, error) {
	i, j := strings.Index(text, "["), strings.LastIndex(text, "]")
	if i < 0 || j <= i {
		return nil, fmt.Errorf("no JSON list in the answer")
	}
	var out []genCandidate
	if err := json.Unmarshal([]byte(text[i:j+1]), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) handleBridgeGenerate(w http.ResponseWriter, r *http.Request) {
	if !a.bridgeReady(w) {
		return
	}
	if _, ok := requireHuman(w, r); !ok {
		return
	}
	var req struct {
		Count int
		Repo  string
	}
	if r.ContentLength != 0 {
		_ = readJSON(r, &req)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	made, source, err := a.generateChallenges(ctx, req.Count, strings.TrimSpace(req.Repo))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "challenges": made, "source": source})
}

// ── Lucille answering in the channel ────────────────────────────

const (
	maxRepliesPer10Min = 6
	maxMachineStreak   = 4 // consecutive lucille/boltz messages before a person must speak
)

// allowReply caps how often Lucille answers on her own, and stops a Lucille <-> Boltz
// exchange from running forever: after maxMachineStreak machine-only messages in a row,
// she waits for a person.
func (b *Bridge) allowReply() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	cut := b.now().Add(-10 * time.Minute)
	kept := b.replies[:0]
	for _, t := range b.replies {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	b.replies = kept
	if len(b.replies) >= maxRepliesPer10Min {
		return false
	}
	streak := 0
	for i := len(b.ring) - 1; i >= 0; i-- {
		ev := b.ring[i]
		if ev.Kind != "say" {
			continue
		}
		if ev.Who != "lucille" && ev.Who != "boltz" {
			break
		}
		streak++
	}
	if streak >= maxMachineStreak {
		return false
	}
	b.replies = append(b.replies, b.now())
	return true
}

func (b *Bridge) transcriptLocked(n int) string {
	var lines []string
	for i := len(b.ring) - 1; i >= 0 && len(lines) < n; i-- {
		if ev := b.ring[i]; ev.Kind == "say" {
			lines = append(lines, ev.Who+": "+ev.Text)
		}
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}

func (a *App) lucilleReply(trigger BridgeEvent, fromAgent bool) {
	b := a.bridge
	if trigger.Who == "lucille" || !b.allowReply() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	b.mu.Lock()
	transcript := b.transcriptLocked(12)
	b.mu.Unlock()
	text, err := a.ask(ctx, bridgeChatSystem, "Conversation so far:\n"+transcript+"\n\nReply as Lucille.", 300)
	text = strings.TrimSpace(text)
	if err != nil || text == "" {
		if fromAgent {
			return // no model, and no person waiting on an answer
		}
		text = "Noted, and it's in the transcript. I can't reach a language model right now, so I can't answer properly."
	}
	to := "all"
	if fromAgent {
		to = "boltz"
	}
	b.emit(BridgeEvent{Who: "lucille", To: to, Kind: "say", Text: text})
}
