package main

// Lucille starts the coders herself (user 2026-10-14: "may Lucille start Claude or Antigravity for prompts I've
// approved?" "yes"). Rules, all enforced here:
//   - only for a prompt the person approved with "Approve and start it now"; nothing starts on its own;
//   - one launched job at a time, 30 minutes at most (then the whole process group is killed);
//   - never when two Claude sessions are already open (the user's plan allows two): the prompt stays in the mailbox;
//   - the coder works in its project's own directory, may edit files and run builds/tests/read-only git, and may NOT
//     commit, push, delete, use sudo or reach the network except Lucille's own service (the tools are given an
//     explicit allow list and deny list; there is nobody to answer a permission prompt in a headless run);
//   - the person can take a running job back at any time (it is killed);
//   - kill switch: touch ~/.config/lucille-assistant/launch.disabled or set LUCILLE_LAUNCH=0.
// What it said when it finished is posted into the thread (secret-looking strings removed).

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	launchTimeout    = 30 * time.Minute
	maxClaudeWindows = 2
	launchOutCap     = 1 << 20
)

var (
	launchQ     = make(chan string, 32)
	launchOnce  sync.Once
	runningMu   sync.Mutex
	runningCmds = map[string]*exec.Cmd{}

	claudeAllow = "Read,Glob,Grep,Edit,Write,Bash(go build:*),Bash(go test:*),Bash(go vet:*),Bash(gofmt:*),Bash(git status:*),Bash(git diff:*),Bash(git log:*),Bash(ls:*)," +
		"Bash(curl -s -X POST http://127.0.0.1:8403/api/chat/i30ltz:*),Bash(curl -s -X POST http://127.0.0.1:8403/api/jobs/:*)"
	claudeDeny = "Bash(sudo:*),Bash(rm:*),Bash(git push:*),Bash(git commit:*),Bash(git reset:*),Bash(git checkout:*),Bash(chmod:*),Bash(dd:*),Bash(mkfs:*)"

	launchFooter = "\n\nYOU ARE RUNNING UNATTENDED (started by Lucille, approved by the user). Nobody can answer a question or approve a command. " +
		"Make small, safe, reviewable changes inside this project directory only. Do not commit or push (auto-sync does that). " +
		"If you are blocked or something needs the user's decision, stop and say exactly what you need. Finish within 30 minutes. " +
		"Your last message is posted to the user's thread, so make it a plain summary: what you changed, what you checked, what is left."
)

func launchEnabled() bool {
	if os.Getenv("LUCILLE_LAUNCH") == "0" {
		return false
	}
	home, _ := os.UserHomeDir()
	_, err := os.Stat(filepath.Join(home, ".config/lucille-assistant/launch.disabled"))
	return err != nil
}

// launchTool maps a mailbox name to the command-line tool that can run it headless ("" = cannot).
func launchTool(to string) string {
	switch to {
	case "claude":
		return "claude"
	case "antigravity":
		return "agy"
	}
	return ""
}

func countLive(tool string) int {
	n := 0
	for _, s := range liveSessions() {
		if s.Tool == tool {
			n++
		}
	}
	return n
}

func (a *App) enqueueLaunch(id string) {
	launchOnce.Do(func() { go a.launchWorker() })
	select {
	case launchQ <- id:
	default:
		log.Printf("launcher: queue full, %s stays in the mailbox", id)
	}
}

func (a *App) launchWorker() {
	for id := range launchQ {
		a.runLaunched(id)
	}
}

// capBuf keeps the first launchOutCap bytes and drops the rest, so a runaway tool cannot fill memory.
type capBuf struct {
	bytes.Buffer
}

func (c *capBuf) Write(p []byte) (int, error) {
	if c.Len() < launchOutCap {
		room := launchOutCap - c.Len()
		if len(p) > room {
			c.Buffer.Write(p[:room])
		} else {
			c.Buffer.Write(p)
		}
	}
	return len(p), nil
}

func (a *App) say(j Job, by, text string, failed bool) {
	_, _ = a.appendThread(ThreadMsg{Anchor: j.Anchor, By: by, Kind: "coder", Text: clip(redact(text), threadMaxText), Quote: j.Quote, Err: failed})
}

func (a *App) runLaunched(id string) {
	var job Job
	for _, j := range a.readJobs() {
		if j.ID == id {
			job = j
		}
	}
	if job.ID == "" || job.Status != "queued" {
		return // taken back, or a session already picked it up
	}
	tool := launchTool(job.To)
	_, dir := a.projectDirFor(job.Anchor)
	switch {
	case !launchEnabled():
		a.say(job, "lucille", "Starting coders myself is switched off, so this prompt waits in the mailbox for a session.", true)
		return
	case tool == "" || dir == "":
		a.say(job, "lucille", "I can't tell which project this belongs to, so I can't start it myself. It waits in the mailbox for a session.", true)
		return
	case tool == "claude" && countLive("claude") >= maxClaudeWindows:
		a.say(job, "lucille", "Two Claude sessions are already open, so I didn't start a third. The prompt stays in the mailbox for one of them.", false)
		return
	}
	bin, err := exec.LookPath(tool)
	if err != nil {
		home, _ := os.UserHomeDir()
		bin = filepath.Join(home, ".local/bin", tool)
	}
	prompt := job.Prompt + launchFooter

	ctx, cancel := context.WithTimeout(context.Background(), launchTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if tool == "claude" {
		cmd = exec.CommandContext(ctx, bin, "-p", "--permission-mode", "acceptEdits", "--allowedTools", claudeAllow, "--disallowedTools", claudeDeny, "--output-format", "text")
		cmd.Stdin = strings.NewReader(prompt)
	} else {
		cmd = exec.CommandContext(ctx, bin, "--mode", "accept-edits", "--print-timeout", "29m", "--print="+prompt)
	}
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) } // the whole group, not only the parent
	home, _ := os.UserHomeDir()
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(home, ".local/bin")+":"+os.Getenv("PATH"), "LUCILLE_JOB_ID="+id)
	var out, errb capBuf
	cmd.Stdout, cmd.Stderr = &out, &errb

	who := job.To + "-headless"
	_, _, _ = a.updateJob(id, func(j *Job) error { j.Status, j.Claimed = "running", who; return nil })
	a.say(job, who, fmt.Sprintf("Started by Lucille in %s. I'll post what it says when it finishes (30 minutes at most; you can take it back any time).", dir), false)

	if err := cmd.Start(); err != nil {
		_, _, _ = a.updateJob(id, func(j *Job) error { j.Status, j.Result = "failed", err.Error(); return nil })
		a.say(job, who, "I couldn't start it: "+err.Error(), true)
		return
	}
	runningMu.Lock()
	runningCmds[id] = cmd
	runningMu.Unlock()
	werr := cmd.Wait()
	runningMu.Lock()
	delete(runningCmds, id)
	runningMu.Unlock()

	var cur Job
	for _, j := range a.readJobs() {
		if j.ID == id {
			cur = j
		}
	}
	text := strings.TrimSpace(out.String())
	switch {
	case cur.Status == "cancelled":
		a.say(job, who, "Stopped: you took it back.", false)
	case ctx.Err() == context.DeadlineExceeded:
		_, _, _ = a.updateJob(id, func(j *Job) error { j.Status, j.Result = "failed", "ran out of time (30 minutes)"; return nil })
		a.say(job, who, "I stopped it after 30 minutes. "+clip(text, 600), true)
	case werr != nil:
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = werr.Error()
		}
		_, _, _ = a.updateJob(id, func(j *Job) error { j.Status, j.Result = "failed", clip(redact(msg), 500); return nil })
		a.say(job, who, "It stopped with an error: "+clip(msg, 600), true)
	default:
		if text == "" {
			text = "(it finished without saying anything)"
		}
		_, _, _ = a.updateJob(id, func(j *Job) error { j.Status, j.Result = "done", clip(redact(text), threadMaxText); return nil })
		a.say(job, who, text, false)
	}
}

// stopRunning kills a launched job's process group (the person took it back).
func stopRunning(id string) bool {
	runningMu.Lock()
	cmd := runningCmds[id]
	runningMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return false
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	return true
}
