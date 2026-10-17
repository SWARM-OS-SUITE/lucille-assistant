package main

import (
	"log"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// The bridge's policy lives here, in Lucille's config, and nowhere else. Dr. Boltz's
// worker asks Lucille for it (GET /api/bridge/inbox) on every poll, so the agent can
// never widen its own permissions: a challenge names a repo by key, never by path, and
// the budget, time limit and per-repo scope all come from this file.

// BridgeRepo is one git repository Dr. Boltz may work in. It is never edited in place:
// the worker adds a throw-away worktree under WorkDir on a branch named boltz/<challenge>,
// edits and commits there, and only a person's approval lets it fast-forward the branch
// into the repo's main branch.
type BridgeRepo struct {
	Name string `json:"name"` // the key a challenge uses: "lucille", "boltz"
	Path string `json:"path"` // the checkout Dr. Boltz branches from
	// Scope limits where a change may land, relative to the repo root. A diff that
	// touches anything outside it is discarded. Empty means the whole repo.
	Scope string `json:"scope,omitempty"`
	// WorkDir holds the throw-away worktrees. Keep it on the same partition as Path
	// (the partition rules), and out of any tree an auto-sync job commits.
	WorkDir string `json:"work_dir"`
	// EnvFile is sourced before the engine and Verify run (toolchain paths).
	EnvFile string `json:"env_file,omitempty"`
	// Verify is a shell snippet run in the worktree (inside Scope) after the engine
	// finishes. The branch is committed only if it exits 0. Written by you, here;
	// never taken from a challenge.
	Verify string `json:"verify,omitempty"`
	Desc   string `json:"desc"`
}

type BridgeConfig struct {
	Repos        []BridgeRepo `json:"repos"`
	MaxBudgetUSD float64      `json:"max_budget_usd"` // spend cap for one challenge
	MaxMinutes   int          `json:"max_minutes"`    // wall-clock cap for one challenge
	Model        string       `json:"model,omitempty"`
}

func defaultBridgeConfig() BridgeConfig {
	return BridgeConfig{
		MaxBudgetUSD: 2,
		MaxMinutes:   20,
		Repos: []BridgeRepo{
			{
				Name:    "lucille",
				Path:    "/mnt/lucillebuild/src/LucilleAssistant",
				WorkDir: "/mnt/lucillebuild/cache/boltz-work",
				EnvFile: "/mnt/lucillebuild/env.sh",
				Verify:  `test -z "$(gofmt -l .)" && go vet ./... && go test ./...`,
				Desc:    "Lucille Assistant: a Go service (standard library only) with an embedded web UI. Tests: go test ./...",
			},
			{
				Name:    "boltz",
				Path:    "/mnt/recoverybuild",
				Scope:   "30-source/mad-dr-boltz",
				WorkDir: "/mnt/recoverybuild/.boltz-work",
				Verify:  `python3 -I -m py_compile brain/boltz_server.py brain/boltz_bridge.py && python3 -I -m unittest discover -s brain -p 'test_*.py'`,
				Desc:    "Dr. Boltz: the Python backend under 30-source/mad-dr-boltz in the Recovery OS repo. Standard library only for new code.",
			},
		},
	}
}

var repoKey = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)

// normalizeBridge validates the hand-edited config. As with actions, one bad entry is
// logged and skipped; it must never stop the service from starting.
func normalizeBridge(b BridgeConfig) BridgeConfig {
	d := defaultBridgeConfig()
	if b.MaxBudgetUSD < 0.1 || b.MaxBudgetUSD > 20 {
		b.MaxBudgetUSD = d.MaxBudgetUSD
	}
	if b.MaxMinutes < 1 || b.MaxMinutes > 120 {
		b.MaxMinutes = d.MaxMinutes
	}
	seen := map[string]bool{}
	var repos []BridgeRepo
	for _, r := range b.Repos {
		r.Scope = strings.Trim(path.Clean(filepath.ToSlash(strings.TrimSpace(r.Scope))), "/")
		if r.Scope == "." {
			r.Scope = ""
		}
		switch {
		case !repoKey.MatchString(r.Name):
			log.Printf("bridge: repo %q: invalid name (lowercase letters, digits, dashes, max 20) — skipped", r.Name)
		case seen[r.Name]:
			log.Printf("bridge: repo %q: duplicate — kept the first", r.Name)
		case !filepath.IsAbs(r.Path) || !filepath.IsAbs(r.WorkDir):
			log.Printf("bridge: repo %q: path and work_dir must be absolute — skipped", r.Name)
		case r.Scope == ".." || strings.HasPrefix(r.Scope, "../"):
			log.Printf("bridge: repo %q: scope must stay inside the repo — skipped", r.Name)
		default:
			seen[r.Name] = true
			repos = append(repos, r)
		}
	}
	b.Repos = repos
	return b
}

func (b BridgeConfig) repo(name string) (BridgeRepo, bool) {
	for _, r := range b.Repos {
		if r.Name == name {
			return r, true
		}
	}
	return BridgeRepo{}, false
}
