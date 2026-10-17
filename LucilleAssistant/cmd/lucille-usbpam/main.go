// lucille-usbpam: "sudo with my phone plugged in". Run by pam_exec.so as root from a `sufficient` line in /etc/pam.d/sudo.
// See internal/usbsudo for the trust model. In short: if (and only if) one of the allow-listed phones is physically
// attached over USB, it asks for the 6-digit code shown in Lucille Mobile on that phone. A right code lets sudo through;
// anything else, no phone, empty answer, three wrong codes, no terminal, exits non-zero so sudo asks for the
// password exactly as it always did. Nothing here uses the network.
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"swarm-os/lucille-assistant/internal/usbsudo"
)

// skipOwnerCheck is "yes" only in a test build made with -ldflags "-X main.skipOwnerCheck=yes"; the shipped binary always checks.
var skipOwnerCheck = "no"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// prompter is the terminal, so the flow can be tested without one.
type prompter interface {
	Say(msg string)
	Ask(msg string) (string, error) // no echo
}

type config struct {
	dir, sys, state string
	checkOwner      bool // false only in tests
	now             func() time.Time
	pause           time.Duration
}

func approve(c config, p func() (prompter, func(), error)) bool {
	conf, err := os.ReadFile(filepath.Join(c.dir, "usb-sudo.conf"))
	if err != nil {
		return false
	}
	allow, err := usbsudo.ParseAllow(string(conf))
	if err != nil {
		return false
	}
	secretPath := filepath.Join(c.dir, "usb-sudo.secret")
	owner := func(fi os.FileInfo) (uint32, bool) {
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return 0, false
		}
		return st.Uid, true
	}
	if c.checkOwner {
		if err := usbsudo.CheckSecretFile(secretPath, 0, owner); err != nil {
			return false
		}
	}
	raw, err := os.ReadFile(secretPath)
	if err != nil {
		return false
	}
	secret, err := usbsudo.DecodeSecret(string(raw))
	if err != nil {
		return false
	}
	dev, ok := usbsudo.Present(c.sys, allow)
	if !ok {
		return false // no phone plugged in: stay out of the way, password prompt follows
	}
	term, done, err := p()
	if err != nil {
		return false
	}
	defer done()
	term.Say(fmt.Sprintf("Phone found: %s.", dev.Label))
	for try := 0; try < 3; try++ {
		code, err := term.Ask("Code from Lucille on your phone (Enter = use password): ")
		if err != nil || strings.TrimSpace(code) == "" {
			return false
		}
		if usbsudo.Accept(secret, code, c.now(), c.state) {
			return true
		}
		term.Say("That code didn't work.")
		time.Sleep(c.pause)
	}
	return false
}

// ttyPrompter talks to the terminal sudo is running in (PAM_TTY), with echo off for the code.
type ttyPrompter struct {
	f    *os.File
	r    *bufio.Reader
	path string
}

func openTTY(path string) (prompter, func(), error) {
	if path == "" {
		return nil, nil, fmt.Errorf("no terminal")
	}
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	return &ttyPrompter{f: f, r: bufio.NewReader(f), path: path}, func() { f.Close() }, nil
}

func (t *ttyPrompter) Say(msg string) { fmt.Fprintln(t.f, msg) }

func (t *ttyPrompter) Ask(msg string) (string, error) {
	fmt.Fprint(t.f, msg)
	_ = exec.Command("stty", "-F", t.path, "-echo").Run()
	defer func() { _ = exec.Command("stty", "-F", t.path, "echo").Run(); fmt.Fprintln(t.f) }()
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() { s, err := t.r.ReadString('\n'); ch <- res{s, err} }()
	select {
	case r := <-ch:
		if r.err != nil && r.err != io.EOF {
			return "", r.err
		}
		return r.s, nil
	case <-time.After(30 * time.Second):
		return "", fmt.Errorf("timed out")
	}
}

func main() {
	if os.Getenv("PAM_USER") == "" {
		fmt.Fprintln(os.Stderr, "lucille-usbpam: PAM_USER is not set; pam_exec should provide it")
		os.Exit(1)
	}
	c := config{
		dir:        env("LUCILLE_USBSUDO_DIR", "/etc/lucille"),
		sys:        env("LUCILLE_USBSUDO_SYS", "/sys/bus/usb/devices"),
		state:      env("LUCILLE_USBSUDO_STATE", "/run/lucille-usbsudo/last-step"),
		checkOwner: skipOwnerCheck != "yes",
		now:        time.Now,
		pause:      time.Second,
	}
	if approve(c, func() (prompter, func(), error) { return openTTY(os.Getenv("PAM_TTY")) }) {
		os.Exit(0)
	}
	os.Exit(1)
}
