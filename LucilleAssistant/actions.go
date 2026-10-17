package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Actions are a small, fixed allowlist of PC-control commands a phone can trigger —
// "restart this service", "drop RAM caches", "reboot" — never an arbitrary string.
//
// Why fixed, not a shell string from the phone: a command typed on a phone and run as
// root on the PC is a remote-root primitive regardless of how politely it is asked for.
// Each Action's Command is a literal argv baked into config.json by whoever runs this
// service; the phone can only pick one by id and flip a confirm flag. No endpoint can
// create, edit or delete an action, and no request field is ever substituted into a
// command. A Sudo action runs under `sudo -n`, which fails loudly instead of prompting
// if the matching NOPASSWD line in /etc/sudoers.d is missing — see `sudoers-template`.
// No sudo password is ever read, stored or sent over the network by this service.

type Action struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Command     []string `json:"command"`
	Sudo        bool     `json:"sudo"`
	Destructive bool     `json:"destructive"`
	// AgentAuto lets Dr. Boltz's agent run this action without a person tapping approve.
	// Off by default; never honoured for a destructive action. Without it the agent can
	// only ask, and the request waits for a person (see bridge.go).
	AgentAuto      bool `json:"agent_auto,omitempty"`
	TimeoutSeconds int  `json:"timeout_seconds,omitempty"`
}

var actionID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

const (
	defaultActionTimeout = 20 * time.Second
	maxActionOutput      = 1 << 16 // 64 KiB; actions are fixed diagnostic/control commands, not log dumps
	auditSnippetLen      = 300
)

func defaultActions() []Action {
	return []Action{
		{ID: "check-mounts", Label: "Check mounts", Sudo: false, AgentAuto: true,
			Description: "Read-only: run check_mounts.sh and report any problem.",
			Command:     []string{"/usr/bin/bash", "/mnt/Lucy/70-scripts/check_mounts.sh"}},
		{ID: "kernel-warnings", Label: "Kernel warnings", Sudo: true, AgentAuto: true,
			Description: "Read-only: kernel errors and warnings from dmesg (needs root on Debian). For Dr. Boltz's diagnostics.",
			Command:     []string{"/usr/bin/dmesg", "--level=err,warn", "--ctime"}},
		{ID: "boot-errors", Label: "Errors this boot", Sudo: true, AgentAuto: true, TimeoutSeconds: 30,
			Description: "Read-only: the last 200 error-level journal lines of this boot. For Dr. Boltz's diagnostics.",
			Command:     []string{"/usr/bin/journalctl", "-p", "err", "-b", "--no-pager", "-n", "200"}},
		{ID: "restart-lucyserver", Label: "Restart LucyServer", Sudo: false,
			Description: "Restart the swarm-lucy-dev user service (port 5000).",
			Command:     []string{"/usr/bin/systemctl", "--user", "restart", "swarm-lucy-dev"}},
		{ID: "drop-ram-caches", Label: "Optimize RAM", Sudo: true,
			Description: "Reclaim cached memory (sysctl vm.drop_caches=1). Safe and reversible; nothing is deleted.",
			Command:     []string{"/usr/bin/sysctl", "-w", "vm.drop_caches=1"}},
		{ID: "reboot-pc", Label: "Reboot this PC", Sudo: true, Destructive: true,
			Description: "Reboot this machine now. Closes every open program without saving.",
			Command:     []string{"/usr/bin/systemctl", "reboot"}},
		{ID: "shutdown-pc", Label: "Shut down this PC", Sudo: true, Destructive: true,
			Description: "Power this machine off now. You will need physical access to turn it back on.",
			Command:     []string{"/usr/bin/systemctl", "poweroff"}},
	}
}

// normalizeActions validates and de-duplicates (first entry with an id wins). Config load
// must never crash the service over one bad hand-edit, so problems are logged and skipped.
func normalizeActions(in []Action) []Action {
	seen := map[string]bool{}
	var out []Action
	for _, a := range in {
		switch {
		case !actionID.MatchString(a.ID):
			log.Printf("actions: %q: invalid id (lowercase letters, digits, dashes, max 40 chars) — skipped", a.ID)
		case seen[a.ID]:
			log.Printf("actions: %q: duplicate id — kept the first, skipped this one", a.ID)
		case len(a.Command) == 0:
			log.Printf("actions: %q: empty command — skipped", a.ID)
		case !filepath.IsAbs(a.Command[0]):
			log.Printf("actions: %q: command must start with an absolute path, got %q — skipped", a.ID, a.Command[0])
		default:
			if a.Destructive && a.AgentAuto {
				log.Printf("actions: %q: destructive actions can never be agent_auto — ignoring that flag", a.ID)
				a.AgentAuto = false
			}
			seen[a.ID] = true
			out = append(out, a)
		}
	}
	return out
}

func actionByID(list []Action, id string) (Action, bool) {
	if !actionID.MatchString(id) {
		return Action{}, false
	}
	for _, a := range list {
		if a.ID == id {
			return a, true
		}
	}
	return Action{}, false
}

// buildArgv is pure and separately tested: it is the one place a Sudo action gets the
// `sudo -n --` prefix. "-n" refuses to prompt (fails instead of hanging with no tty);
// "--" stops sudo's own flag parsing before the target command.
func buildArgv(a Action) []string {
	cmd := append([]string{}, a.Command...) // never alias the caller's slice
	if !a.Sudo {
		return cmd
	}
	return append([]string{"/usr/bin/sudo", "-n", "--"}, cmd...)
}

// minimalActionEnv is deliberately short: PATH/HOME/USER/LANG only, so a fixed command
// that happens to print its environment (or a script it calls) cannot leak an API key.
func minimalActionEnv() []string {
	env := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}
	if home, err := os.UserHomeDir(); err == nil {
		env = append(env, "HOME="+home)
	}
	if u := os.Getenv("USER"); u != "" {
		env = append(env, "USER="+u)
	}
	return env
}

// ActionResult is one run's outcome.
type ActionResult struct {
	OK         bool   `json:"ok"`
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output"`
	Truncated  bool   `json:"truncated,omitempty"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// runAction runs one action to completion or its timeout, whichever comes first. Combined
// stdout+stderr is capped; a command that keeps writing past the cap is still killed on
// timeout, it just doesn't grow the response forever.
func runAction(parent context.Context, a Action) ActionResult {
	timeout := defaultActionTimeout
	if a.TimeoutSeconds > 0 {
		timeout = time.Duration(a.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	argv := buildArgv(a)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = minimalActionEnv()
	var buf bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &buf, max: maxActionOutput}
	cmd.Stderr = cmd.Stdout

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)
	res := ActionResult{DurationMS: dur.Milliseconds(), Output: buf.String(), Truncated: buf.Len() >= maxActionOutput}

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.TimedOut = true
		res.ExitCode = -1
		res.Error = fmt.Sprintf("timed out after %s", timeout)
	case err != nil:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
			res.Error = err.Error() // e.g. the binary itself was not found
		}
	default:
		res.OK = true
	}
	return res
}

// limitedWriter stops copying once max bytes have been written, without erroring the
// command (a chatty process should not fail the action just for talking too much).
type limitedWriter struct {
	w   *bytes.Buffer
	max int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.w.Len() < l.max {
		room := l.max - l.w.Len()
		if room > len(p) {
			room = len(p)
		}
		l.w.Write(p[:room])
	}
	return len(p), nil
}

// ── Audit log ───────────────────────────────────────────────────

type actionLogEntry struct {
	TS         time.Time    `json:"ts"`
	ActionID   string       `json:"action_id"`
	Confirmed  bool         `json:"confirmed"`
	RemoteAddr string       `json:"remote_addr"`
	Result     ActionResult `json:"result"`
}

func (s *Store) actionsLogPath() string { return filepath.Join(s.dir, "actions.log") }

// AppendActionLog is append-only and best-effort: a logging failure must never be the
// reason a diagnostic action looked like it failed, so the caller only gets the error back
// to put in its own server log.
func (s *Store) AppendActionLog(e actionLogEntry) error {
	// Output can be long and is already in the HTTP response; keep the log line to a
	// bounded snippet so the file stays skimmable and never grows unbounded.
	if len(e.Result.Output) > auditSnippetLen {
		e.Result.Output = e.Result.Output[:auditSnippetLen] + "…"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.actionsLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = f.Write(b)
	return err
}

// ── HTTP ────────────────────────────────────────────────────────

// actionRunLock serializes every action across the whole service: these are PC-control
// commands (including power), and two running at once is never something to allow by
// accident. A stuck one still frees itself at its own timeout.
var actionRunLock sync.Mutex

type actionView struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Command     string `json:"command"` // shown so the phone can display exactly what will run
	Sudo        bool   `json:"sudo"`
	Destructive bool   `json:"destructive"`
	AgentAuto   bool   `json:"agent_auto,omitempty"`
}

func toActionView(a Action) actionView {
	cmd := strings.Join(a.Command, " ")
	if a.Sudo {
		cmd = "sudo " + cmd
	}
	return actionView{ID: a.ID, Label: a.Label, Description: a.Description, Command: cmd, Sudo: a.Sudo, Destructive: a.Destructive, AgentAuto: a.AgentAuto}
}

func (a *App) handleActions(w http.ResponseWriter, r *http.Request) {
	views := make([]actionView, 0, len(a.cfg.Actions))
	for _, act := range a.cfg.Actions {
		views = append(views, toActionView(act))
	}
	writeJSON(w, map[string]any{"ok": true, "actions": views})
}

func (a *App) handleActionRun(w http.ResponseWriter, r *http.Request) {
	if c := callerOf(r); c.IsAgent() || c.BadToken {
		// Dr. Boltz asks through the bridge so a person sees the request, and so what it
		// may run unattended is decided per action (agent_auto), not by reaching this route.
		writeErr(w, http.StatusForbidden, "Dr. Boltz requests actions through /api/bridge/actions/{id}/request.")
		return
	}
	act, found := actionByID(a.cfg.Actions, r.PathValue("id"))
	if !found {
		writeErr(w, http.StatusNotFound, "No such action.")
		return
	}
	var req struct{ Confirm bool }
	if r.ContentLength != 0 {
		if err := readJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, `Send {} or {"confirm": true}.`)
			return
		}
	}
	if act.Destructive && !req.Confirm {
		writeJSON(w, map[string]any{"ok": false, "needs_confirm": true,
			"error": fmt.Sprintf("%s Resend with {\"confirm\": true} to go ahead.", act.Description)})
		return
	}
	res, busy := a.runLogged(r.Context(), act, req.Confirm, r.RemoteAddr)
	if busy {
		writeErr(w, http.StatusConflict, "Another action is already running on this PC. Wait for it to finish.")
		return
	}
	writeJSON(w, res)
}

// runLogged is the one place an action actually runs: serialized PC-wide, audit-logged.
// busy is true when another action holds the lock (nothing ran).
func (a *App) runLogged(ctx context.Context, act Action, confirmed bool, remote string) (res ActionResult, busy bool) {
	if !actionRunLock.TryLock() {
		return ActionResult{}, true
	}
	defer actionRunLock.Unlock()
	log.Printf("actions: running %q (sudo=%v destructive=%v confirm=%v) for %s", act.ID, act.Sudo, act.Destructive, confirmed, remote)
	res = runAction(ctx, act)
	if err := a.store.AppendActionLog(actionLogEntry{TS: time.Now().UTC(), ActionID: act.ID, Confirmed: confirmed, RemoteAddr: remote, Result: res}); err != nil {
		log.Printf("actions: audit log: %v", err)
	}
	return res, false
}

// ── `lucille-assistant sudoers-template` (run on this PC) ──────

// runSudoersTemplate prints one NOPASSWD line per configured Sudo action, for the user to
// review and install themselves (sudo -n means nothing runs as root until they do).
func runSudoersTemplate(actions []Action, out *strings.Builder) error {
	u, err := user.Current()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "# Generated by `lucille-assistant sudoers-template` — review before installing.\n")
	fmt.Fprintf(out, "# Grants %s passwordless root for exactly these commands, nothing else.\n", u.Username)
	fmt.Fprintf(out, "# Install (checks syntax first; installs nothing if that check fails):\n")
	fmt.Fprintf(out, "#   lucille-assistant sudoers-template > /tmp/lucille-assistant.sudoers\n")
	fmt.Fprintf(out, "#   sudo visudo -c -f /tmp/lucille-assistant.sudoers && \\\n")
	fmt.Fprintf(out, "#     sudo install -o root -g root -m 0440 /tmp/lucille-assistant.sudoers /etc/sudoers.d/lucille-assistant\n\n")
	n := 0
	for _, act := range actions {
		if !act.Sudo {
			continue
		}
		why := "a person approves each use (phone or PC)"
		if act.AgentAuto {
			why = "Dr. Boltz may run this unattended (agent_auto)"
		}
		if act.Destructive {
			why = "DESTRUCTIVE: needs confirm; delete this line if you do not want it reachable from a phone"
		}
		fmt.Fprintf(out, "# %s: %s\n", act.ID, why)
		fmt.Fprintf(out, "%s ALL=(root) NOPASSWD: %s\n", u.Username, strings.Join(act.Command, " "))
		n++
	}
	if n == 0 {
		fmt.Fprintf(out, "# (no action in config.json has \"sudo\": true — nothing to grant)\n")
	}
	return nil
}

func runActionsCLI(args []string, out *strings.Builder) int {
	cfg := loadConfig()
	switch {
	case len(args) == 1 && args[0] == "sudoers-template":
		if err := runSudoersTemplate(cfg.Actions, out); err != nil {
			fmt.Fprintln(out, "error:", err)
			return 1
		}
		return 0
	case len(args) == 1 && args[0] == "list":
		for _, a := range cfg.Actions {
			fmt.Fprintf(out, "%-20s sudo=%-5v destructive=%-5v %s\n", a.ID, a.Sudo, a.Destructive, a.Description)
		}
		return 0
	default:
		fmt.Fprintln(out, "usage: lucille-assistant sudoers-template | list")
		return 2
	}
}
