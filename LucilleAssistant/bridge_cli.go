package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// `lucille-assistant bridge ...` runs on this PC and talks to the running service over
// loopback, the same way `pair` does. It is the person's console for the bridge.

const bridgeTmuxSession = "lucille-boltz"

const bridgeUsage = `usage: lucille-assistant bridge <command>
  status                          Dr. Boltz online?, the queue, anything waiting for you
  challenge TITLE [--repo R] [--body TEXT]   queue a challenge yourself
  generate [N] [--repo R]         ask Lucille to think up N challenges
  say TEXT [--to lucille|boltz|all]          talk in the shared channel
  approve|reject|cancel|retry ID  decide on a challenge (approve lets Dr. Boltz merge it)
  approvals                       list Dr. Boltz's requests to run something as root
  allow|deny ID                   answer one of those requests
  tail [--no-follow]              the live transcript (what the phones and Dr. Boltz see)
  term [--ro]                     the tmux mirror: transcript on top, a shell below`

func bridgeCall(method, path string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, loopbackAPIBase(loadConfig())+path, rd)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return 0, fmt.Errorf("could not reach the running service: %w (systemctl --user status lucille-assistant)", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var env struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &env)
	if !env.OK {
		msg := env.Error
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return resp.StatusCode, fmt.Errorf("%s", msg)
	}
	if out != nil {
		return resp.StatusCode, json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}

// takeFlag removes "--name value" from args and returns the value.
func takeFlag(args []string, name string) (string, []string) {
	for i := 0; i < len(args); i++ {
		if args[i] == name && i+1 < len(args) {
			return args[i+1], append(append([]string{}, args[:i]...), args[i+2:]...)
		}
	}
	return "", args
}

func hasFlag(args []string, name string) ([]string, bool) {
	for i, a := range args {
		if a == name {
			return append(append([]string{}, args[:i]...), args[i+1:]...), true
		}
	}
	return args, false
}

func runBridgeCLI(args []string, w io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(w, bridgeUsage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	fail := func(err error) int { fmt.Fprintln(w, "error:", err); return 1 }
	switch cmd {
	case "status":
		var st struct {
			Agent struct {
				Online   bool      `json:"online"`
				Label    string    `json:"label"`
				LastSeen time.Time `json:"last_seen"`
			} `json:"agent"`
			Challenges []Challenge `json:"challenges"`
			Approvals  []Approval  `json:"approvals"`
		}
		if _, err := bridgeCall("GET", "/api/bridge", nil, &st); err != nil {
			return fail(err)
		}
		switch {
		case st.Agent.Online:
			fmt.Fprintf(w, "Dr. Boltz: online (%s)\n", st.Agent.Label)
		case !st.Agent.LastSeen.IsZero():
			fmt.Fprintf(w, "Dr. Boltz: offline, last seen %s ago. Start it: python3 boltz_bridge.py run\n", time.Since(st.Agent.LastSeen).Round(time.Second))
		default:
			fmt.Fprintln(w, "Dr. Boltz: never connected. Pair it: lucille-assistant pair --role agent")
		}
		if len(st.Challenges) == 0 {
			fmt.Fprintln(w, "no challenges yet: `bridge generate` or `bridge challenge \"...\"`")
		}
		for _, c := range st.Challenges {
			fmt.Fprintf(w, "%-6s %-9s %-8s %s\n", c.ID, c.Status, c.Repo, c.Title)
		}
		for _, ap := range st.Approvals {
			if ap.Status == "pending" {
				fmt.Fprintf(w, "WAITING FOR YOU  %s  Dr. Boltz wants to run %q (%s): %s   -> bridge allow %s | deny %s\n", ap.ID, ap.Label, ap.Command, ap.Reason, ap.ID, ap.ID)
			}
		}
		return 0
	case "challenge":
		repo, rest := takeFlag(rest, "--repo")
		body, rest := takeFlag(rest, "--body")
		if repo == "" {
			repo = "lucille"
		}
		var out struct {
			Challenge Challenge `json:"challenge"`
		}
		if _, err := bridgeCall("POST", "/api/bridge/challenges", map[string]string{"title": strings.Join(rest, " "), "body": body, "repo": repo}, &out); err != nil {
			return fail(err)
		}
		fmt.Fprintf(w, "queued %s [%s]: %s\n", out.Challenge.ID, out.Challenge.Repo, out.Challenge.Title)
		return 0
	case "generate":
		repo, rest := takeFlag(rest, "--repo")
		n := 3
		if len(rest) > 0 {
			fmt.Sscanf(rest[0], "%d", &n)
		}
		var out struct {
			Challenges []Challenge `json:"challenges"`
			Source     string      `json:"source"`
		}
		if _, err := bridgeCall("POST", "/api/bridge/generate", map[string]any{"count": n, "repo": repo}, &out); err != nil {
			return fail(err)
		}
		for _, c := range out.Challenges {
			fmt.Fprintf(w, "queued %s [%s]: %s\n", c.ID, c.Repo, c.Title)
		}
		if len(out.Challenges) == 0 {
			fmt.Fprintln(w, "nothing new to queue (all of Lucille's ideas were already in the list)")
		}
		fmt.Fprintf(w, "(source: %s)\n", out.Source)
		return 0
	case "say":
		to, rest := takeFlag(rest, "--to")
		if _, err := bridgeCall("POST", "/api/bridge/say", map[string]string{"text": strings.Join(rest, " "), "to": to}, nil); err != nil {
			return fail(err)
		}
		return 0
	case "approve", "reject", "cancel", "retry":
		if len(rest) != 1 {
			fmt.Fprintf(w, "usage: lucille-assistant bridge %s <challenge-id>\n", cmd)
			return 2
		}
		var out struct {
			Challenge Challenge `json:"challenge"`
		}
		if _, err := bridgeCall("POST", "/api/bridge/challenges/"+rest[0]+"/decision", map[string]string{"action": cmd}, &out); err != nil {
			return fail(err)
		}
		fmt.Fprintf(w, "%s is now %s\n", out.Challenge.ID, out.Challenge.Status)
		return 0
	case "approvals":
		var out struct {
			Approvals []Approval `json:"approvals"`
		}
		if _, err := bridgeCall("GET", "/api/bridge/approvals", nil, &out); err != nil {
			return fail(err)
		}
		for _, ap := range out.Approvals {
			fmt.Fprintf(w, "%-14s %-8s %q  %s  (%s)\n", ap.ID, ap.Status, ap.Label, ap.Command, ap.Reason)
		}
		if len(out.Approvals) == 0 {
			fmt.Fprintln(w, "no requests")
		}
		return 0
	case "allow", "deny":
		if len(rest) != 1 {
			fmt.Fprintf(w, "usage: lucille-assistant bridge %s <approval-id>\n", cmd)
			return 2
		}
		var out struct {
			Approval Approval `json:"approval"`
		}
		if _, err := bridgeCall("POST", "/api/bridge/approvals/"+rest[0]+"/decision", map[string]bool{"approve": cmd == "allow"}, &out); err != nil {
			return fail(err)
		}
		fmt.Fprintf(w, "%s: %s\n", out.Approval.Label, out.Approval.Status)
		if out.Approval.Result != nil && out.Approval.Result.Error != "" {
			fmt.Fprintln(w, out.Approval.Result.Error)
		}
		return 0
	case "tail":
		return runBridgeTail(rest, w)
	case "term":
		return runBridgeTerm(rest, w)
	}
	fmt.Fprintln(w, bridgeUsage)
	return 2
}

// ── The mirror ──────────────────────────────────────────────────

const (
	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
)

func whoColor(who string) string {
	switch who {
	case "lucille":
		return "\x1b[95m"
	case "boltz":
		return "\x1b[96m"
	case "system":
		return "\x1b[33m"
	}
	return "\x1b[92m"
}

// formatEvent renders one transcript line. Text is scrubbed again here (the server
// already cleans it) so a hand-edited events.jsonl can't put escape codes on a terminal.
func formatEvent(ev BridgeEvent, color bool) string {
	text := cleanEventText(ev.Text, maxEventText, true)
	who := cleanEventText(ev.Who, 60, false)
	arrow := who
	if ev.To != "" && ev.To != "all" {
		arrow += "→" + ev.To
	}
	text = strings.ReplaceAll(text, "\n", "\n                    ")
	ts := ev.TS.Local().Format("15:04:05")
	if !color {
		return fmt.Sprintf("%s  %-14s %s", ts, arrow, text)
	}
	return fmt.Sprintf("%s%s%s  %s%-14s%s %s", ansiDim, ts, ansiReset, whoColor(who), arrow, ansiReset, text)
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func runBridgeTail(args []string, w io.Writer) int {
	args, noFollow := hasFlag(args, "--no-follow")
	color := isTerminal(w)
	var st struct {
		Events []BridgeEvent `json:"events"`
		Seq    int64         `json:"seq"`
	}
	if _, err := bridgeCall("GET", "/api/bridge", nil, &st); err != nil {
		fmt.Fprintln(w, "error:", err)
		return 1
	}
	last := st.Seq
	evs := st.Events
	if len(evs) > 30 {
		evs = evs[len(evs)-30:]
	}
	for _, ev := range evs {
		fmt.Fprintln(w, formatEvent(ev, color))
	}
	if noFollow {
		return 0
	}
	base := loopbackAPIBase(loadConfig())
	client := &http.Client{} // no timeout: this is a long-lived stream
	for {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/bridge/stream?since=%d", base, last), nil)
		resp, err := client.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			sc := bufio.NewScanner(resp.Body)
			sc.Buffer(make([]byte, 64*1024), 1<<20)
			for sc.Scan() {
				line := sc.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var ev BridgeEvent
				if json.Unmarshal([]byte(line[6:]), &ev) == nil && ev.Seq > last {
					last = ev.Seq
					fmt.Fprintln(w, formatEvent(ev, color))
				}
			}
			resp.Body.Close()
		} else if err == nil {
			resp.Body.Close()
		}
		time.Sleep(2 * time.Second) // the service restarted or the stream dropped: catch up from `last`
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// runBridgeTerm builds (or reattaches to) a tmux session: the live transcript in the top
// pane, an ordinary shell below. Everyone watching the stream sees the same thing; attach
// with --ro to look without being able to type.
func runBridgeTerm(args []string, w io.Writer) int {
	args, ro := hasFlag(args, "--ro")
	_ = args
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		fmt.Fprintln(w, "error: tmux is not installed (sudo apt install tmux)")
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(w, "error:", err)
		return 1
	}
	if exec.Command(tmux, "has-session", "-t", bridgeTmuxSession).Run() != nil {
		steps := [][]string{
			{"new-session", "-d", "-s", bridgeTmuxSession, "-n", "mirror", "-x", "200", "-y", "50", shellQuote(exe) + " bridge tail"},
			{"split-window", "-v", "-l", "30%", "-t", bridgeTmuxSession + ":mirror"},
			{"set-option", "-t", bridgeTmuxSession, "status-left", " lucille <-> boltz "},
			{"select-pane", "-t", bridgeTmuxSession + ":mirror.1"},
		}
		for _, s := range steps {
			if out, err := exec.Command(tmux, s...).CombinedOutput(); err != nil {
				fmt.Fprintf(w, "error: tmux %s: %v %s\n", s[0], err, strings.TrimSpace(string(out)))
				return 1
			}
		}
	}
	argv := []string{"tmux", "attach-session", "-t", bridgeTmuxSession}
	if ro {
		argv = append(argv, "-r")
	}
	if err := syscall.Exec(tmux, argv, os.Environ()); err != nil {
		fmt.Fprintln(w, "error:", err)
		return 1
	}
	return 0
}
