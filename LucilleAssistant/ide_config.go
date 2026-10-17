package main

import (
	"log"
	"path/filepath"
)

// The phone IDE (the Boltz Recovery app's "IDE" tab) edits the PC's repositories in place,
// over this service, so nothing has to be cloned onto the phone. Which repositories it may
// open is decided here and nowhere else: a request names a repo by key, never by path.

// IDERepo is one checkout the phone may browse, edit and commit in. It is the real working
// tree (like opening the folder in VS Code on the PC), not a copy.
type IDERepo struct {
	Name string `json:"name"` // the key the app sends: "lucille", "recovery", "boltz-app"
	Path string `json:"path"` // absolute path of the checkout on this PC
	Desc string `json:"desc"`
}

type IDEConfig struct {
	Repos     []IDERepo `json:"repos"`
	MaxFileKB int       `json:"max_file_kb"` // largest file the editor will open or save
}

func defaultIDEConfig() IDEConfig {
	return IDEConfig{
		MaxFileKB: 1024,
		Repos: []IDERepo{
			{Name: "lucille", Path: "/mnt/lucillebuild/src/LucilleAssistant", Desc: "Lucille Assistant: Go service and web UI"},
			{Name: "recovery", Path: "/mnt/recoverybuild", Desc: "I30LTZ Recovery OS and Dr. Boltz's backend"},
			{Name: "boltz-app", Path: "/mnt/boltzbuild/src/I30LTZ-Recovery-companion", Desc: "Boltz Recovery phone app"},
		},
	}
}

// normalizeIDE validates the hand-edited config; like actions and the bridge, one bad entry
// is logged and skipped rather than stopping the service.
func normalizeIDE(c IDEConfig) IDEConfig {
	if c.MaxFileKB < 16 || c.MaxFileKB > 8192 {
		c.MaxFileKB = defaultIDEConfig().MaxFileKB
	}
	seen := map[string]bool{}
	var repos []IDERepo
	for _, r := range c.Repos {
		switch {
		case !repoKey.MatchString(r.Name):
			log.Printf("ide: repo %q: invalid name (lowercase letters, digits, dashes, max 20) — skipped", r.Name)
		case seen[r.Name]:
			log.Printf("ide: repo %q: duplicate — kept the first", r.Name)
		case !filepath.IsAbs(r.Path):
			log.Printf("ide: repo %q: path must be absolute — skipped", r.Name)
		case filepath.Clean(r.Path) == "/":
			log.Printf("ide: repo %q: refusing to expose the whole filesystem — skipped", r.Name)
		default:
			r.Path = filepath.Clean(r.Path)
			seen[r.Name] = true
			repos = append(repos, r)
		}
	}
	c.Repos = repos
	return c
}

func (c IDEConfig) repo(name string) (IDERepo, bool) {
	for _, r := range c.Repos {
		if r.Name == name {
			return r, true
		}
	}
	return IDERepo{}, false
}
