// Package usbsudo is the logic behind "sudo only with my phone physically plugged in, plus the code on its screen".
//
// Two independent facts, both checked by a root-run helper (cmd/lucille-usbpam, via pam_exec):
//
//  1. Presence: one of the allow-listed phones (USB vendor + serial number) is attached right now. The helper reads
//     /sys/bus/usb/devices, which only the kernel writes, so a program running as the user cannot make it true.
//     A serial number is not a secret (any program can read it with lsusb), so this proves "a phone is here",
//     not "it is MY phone".
//  2. Possession: the 6-digit rotating code (RFC 6238 TOTP) shown in Lucille Mobile on that phone, computed from a
//     secret kept in a root-only file here and, encrypted, on the phone. No network is involved anywhere.
//
// Any failure, an absent phone, a wrong or reused code, no terminal, a badly protected secret file, makes the helper
// exit non-zero, and a `sufficient` PAM line then falls through to the normal password prompt. This can
// add a way in; it can never lock the user out.
package usbsudo

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	Step   = 30 // seconds per code
	Digits = 6
)

// Code is the TOTP for time t (RFC 6238, HMAC-SHA1, 6 digits, 30 s): what authenticator apps compute.
func Code(secret []byte, t time.Time) string { return codeAtStep(secret, t.Unix()/Step) }

func codeAtStep(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := (uint32(sum[off]&0x7f) << 24) | uint32(sum[off+1])<<16 | uint32(sum[off+2])<<8 | uint32(sum[off+3])
	return fmt.Sprintf("%0*d", Digits, n%1000000)
}

// DecodeSecret reads a base32 secret the way authenticator apps show it: any case, spaces and dashes allowed.
func DecodeSecret(s string) ([]byte, error) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "", "\n", "", "\t", "").Replace(s))
	b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
	if err != nil || len(b) < 10 {
		return nil, errors.New("the secret is not valid base32 (or is too short)")
	}
	return b, nil
}

// Accept checks a typed code against the clock (one step either side, for drift) and refuses a step that was
// already used, so a code seen over a shoulder or on a screen works at most once. stateFile holds the last step used.
func Accept(secret []byte, typed string, now time.Time, stateFile string) bool {
	typed = strings.TrimSpace(typed)
	if len(typed) != Digits {
		return false
	}
	last := int64(-1)
	if b, err := os.ReadFile(stateFile); err == nil {
		if v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
			last = v
		}
	}
	cur := now.Unix() / Step
	hit := int64(-1)
	for s := cur - 1; s <= cur+1; s++ { // check all three; don't stop early (constant-ish time)
		if subtle.ConstantTimeCompare([]byte(typed), []byte(codeAtStep(secret, s))) == 1 {
			hit = s
		}
	}
	if hit < 0 || hit <= last {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(stateFile), 0o700); err == nil {
		_ = os.WriteFile(stateFile, []byte(strconv.FormatInt(hit, 10)), 0o600)
	}
	return true
}

// Device identifies a phone by USB vendor id and serial number (both lower-case hex vendor, exact serial).
// The product id is deliberately not matched: it changes with the phone's USB mode (charge / file transfer / debugging).
type Device struct{ Vendor, Serial, Label string }

// ParseAllow reads lines of `phone <vendor-hex> <serial> [label...]`; blank lines and #-comments are ignored.
func ParseAllow(text string) ([]Device, error) {
	var out []Device
	for i, line := range strings.Split(text, "\n") {
		if j := strings.Index(line, "#"); j >= 0 {
			line = line[:j]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if f[0] != "phone" || len(f) < 3 {
			return nil, fmt.Errorf("line %d: want `phone <vendor> <serial> [label]`", i+1)
		}
		d := Device{Vendor: strings.ToLower(f[1]), Serial: f[2], Label: strings.Join(f[3:], " ")}
		if d.Label == "" {
			d.Label = d.Serial
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, errors.New("no phones are allow-listed")
	}
	return out, nil
}

// Present reports the first allow-listed phone currently attached, reading the kernel's USB tree under sysRoot
// (normally /sys/bus/usb/devices).
func Present(sysRoot string, allow []Device) (Device, bool) {
	entries, err := os.ReadDir(sysRoot)
	if err != nil {
		return Device{}, false
	}
	for _, e := range entries {
		dir := filepath.Join(sysRoot, e.Name())
		vid, err1 := os.ReadFile(filepath.Join(dir, "idVendor"))
		ser, err2 := os.ReadFile(filepath.Join(dir, "serial"))
		if err1 != nil || err2 != nil {
			continue
		}
		v, s := strings.ToLower(strings.TrimSpace(string(vid))), strings.TrimSpace(string(ser))
		for _, d := range allow {
			if d.Vendor == v && d.Serial == s {
				return d, true
			}
		}
	}
	return Device{}, false
}

// CheckSecretFile refuses to use a secret that anyone but root can read or replace: a user-writable secret would let
// a user-level program choose its own code.
func CheckSecretFile(path string, wantUID uint32, uid func(os.FileInfo) (uint32, bool)) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must not be readable or writable by anyone but its owner (chmod 600)", path)
	}
	if u, ok := uid(fi); !ok || u != wantUID {
		return fmt.Errorf("%s must be owned by root", path)
	}
	return nil
}
