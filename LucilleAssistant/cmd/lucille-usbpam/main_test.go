package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"swarm-os/lucille-assistant/internal/usbsudo"
)

type fake struct {
	answers []string
	asked   int
	said    []string
}

func (f *fake) Say(m string) { f.said = append(f.said, m) }
func (f *fake) Ask(string) (string, error) {
	f.asked++
	if len(f.answers) == 0 {
		return "", os.ErrClosed
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a, nil
}

var secretB32 = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func setup(t *testing.T, plugged bool) (config, []byte) {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "usb-sudo.conf"), []byte("phone 04e8 R5GYC58ZJCA Samsung\n"), 0o600)
	os.WriteFile(filepath.Join(root, "usb-sudo.secret"), []byte(secretB32+"\n"), 0o600)
	sys := filepath.Join(root, "sys")
	if plugged {
		d := filepath.Join(sys, "3-2")
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "idVendor"), []byte("04e8\n"), 0o644)
		os.WriteFile(filepath.Join(d, "serial"), []byte("R5GYC58ZJCA\n"), 0o644)
	}
	sec, _ := usbsudo.DecodeSecret(secretB32)
	return config{dir: root, sys: sys, state: filepath.Join(root, "run", "last"), now: func() time.Time { return time.Unix(1700000000, 0) }}, sec
}

func term(f *fake) func() (prompter, func(), error) {
	return func() (prompter, func(), error) { return f, func() {}, nil }
}

func TestRightCodeWithPhonePluggedIn(t *testing.T) {
	c, sec := setup(t, true)
	f := &fake{answers: []string{usbsudo.Code(sec, c.now())}}
	if !approve(c, term(f)) {
		t.Fatal("refused a right code with the phone plugged in")
	}
}

func TestNoPhoneMeansNoPromptAndNoApproval(t *testing.T) {
	c, sec := setup(t, false)
	f := &fake{answers: []string{usbsudo.Code(sec, c.now())}}
	if approve(c, term(f)) || f.asked != 0 {
		t.Fatalf("approved or prompted with no phone: asked=%d", f.asked)
	}
}

func TestWrongCodesThreeTimesThenFallThrough(t *testing.T) {
	c, _ := setup(t, true)
	f := &fake{answers: []string{"111111", "222222", "333333", "444444"}}
	if approve(c, term(f)) || f.asked != 3 {
		t.Fatalf("wrong codes: asked=%d", f.asked)
	}
}

func TestEnterAloneUsesPassword(t *testing.T) {
	c, _ := setup(t, true)
	f := &fake{answers: []string{"\n"}}
	if approve(c, term(f)) || f.asked != 1 {
		t.Fatal("empty answer should fall through at once")
	}
}

func TestCodeWorksOnlyOnce(t *testing.T) {
	c, sec := setup(t, true)
	code := usbsudo.Code(sec, c.now())
	if !approve(c, term(&fake{answers: []string{code}})) {
		t.Fatal("first use refused")
	}
	if approve(c, term(&fake{answers: []string{code, code, code}})) {
		t.Fatal("a used code was accepted again")
	}
}

func TestBrokenConfigFallsThrough(t *testing.T) {
	c, sec := setup(t, true)
	os.WriteFile(filepath.Join(c.dir, "usb-sudo.conf"), []byte("garbage\n"), 0o600)
	if approve(c, term(&fake{answers: []string{usbsudo.Code(sec, c.now())}})) {
		t.Fatal("approved with a broken config")
	}
	c2, _ := setup(t, true)
	os.Remove(filepath.Join(c2.dir, "usb-sudo.secret"))
	if approve(c2, term(&fake{})) {
		t.Fatal("approved with no secret")
	}
}

func TestNoTerminalFallsThrough(t *testing.T) {
	c, _ := setup(t, true)
	if approve(c, func() (prompter, func(), error) { return nil, nil, os.ErrNotExist }) {
		t.Fatal("approved without a terminal")
	}
}
