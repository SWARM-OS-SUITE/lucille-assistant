package unlockproto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ErrUnavailable means the service could not be reached or refused the request. The PAM
// helper maps every non-nil error to "fall through to the password", so a dead phone,
// network or service never locks anyone out; it only costs the password prompt.
var ErrUnavailable = errors.New("lucille service unavailable")

var ErrLockedOut = errors.New("too many failed approvals; use your password for a few minutes")

const (
	maxFails     = 3
	failWindow   = 10 * time.Minute
	lockDuration = 5 * time.Minute
)

// Config is everything Approve needs; tests swap the clock, state dir and HTTP client.
type Config struct {
	API        string // e.g. http://127.0.0.1:8403
	PubKeyPath string // root-owned, e.g. /etc/lucille/approver.pub
	StateDir   string // root-owned, e.g. /run/lucille-unlock; holds the failure counter
	Service    string
	User       string
	Host       string
	TTL        time.Duration
	Poll       time.Duration
	Out        io.Writer // shown to the user by pam_exec's "stdout" option
	HTTP       *http.Client
	Now        func() time.Time
}

type failState struct {
	Count       int       `json:"count"`
	First       time.Time `json:"first"`
	LockedUntil time.Time `json:"locked_until"`
}

func (c *Config) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Config) statePath() string { return filepath.Join(c.StateDir, "fails.json") }

func (c *Config) loadFails() failState {
	var s failState
	if c.StateDir == "" {
		return s
	}
	if raw, err := os.ReadFile(c.statePath()); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func (c *Config) saveFails(s failState) {
	if c.StateDir == "" {
		return
	}
	if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
		return
	}
	raw, _ := json.Marshal(s)
	_ = os.WriteFile(c.statePath(), raw, 0o600)
}

// recordFail counts a wrong number or bad signature. Timeouts and unreachable services do
// not count: they are not guesses. Three failures in ten minutes lock phone approval for
// five minutes (the password still works).
func (c *Config) recordFail() {
	s := c.loadFails()
	now := c.now()
	if s.Count == 0 || now.Sub(s.First) > failWindow {
		s = failState{First: now}
	}
	s.Count++
	if s.Count >= maxFails {
		s.LockedUntil = now.Add(lockDuration)
	}
	c.saveFails(s)
}

func (c *Config) clearFails() { c.saveFails(failState{}) }

type resultReply struct {
	State  string `json:"state"` // pending | answered | expired | unknown
	Chosen string `json:"chosen"`
	Sig    string `json:"sig"`
}

// Approve runs one approval round. nil means the enrolled phone approved this exact
// request; any error means "do not approve" and the caller falls back to the password.
func Approve(ctx context.Context, cfg Config) error {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 5 * time.Second}
	}
	if cfg.Poll <= 0 {
		cfg.Poll = 700 * time.Millisecond
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 45 * time.Second
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	now := cfg.now()
	if s := cfg.loadFails(); now.Before(s.LockedUntil) {
		return ErrLockedOut
	}
	keyPEM, err := os.ReadFile(cfg.PubKeyPath)
	if err != nil {
		return fmt.Errorf("no enrolled phone key: %w", err)
	}
	pub, err := ParsePublicKey(keyPEM)
	if err != nil {
		return fmt.Errorf("enrolled phone key unusable: %w", err)
	}
	req, code, err := NewRequest(cfg.Service, cfg.User, cfg.Host, cfg.TTL, now)
	if err != nil {
		return err
	}

	body, _ := json.Marshal(req)
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, cfg.API+"/api/unlock/request", bytes.NewReader(body))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := cfg.HTTP.Do(hreq)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
	}

	// The code appears here and nowhere else: not in the request, not in any log.
	fmt.Fprintf(cfg.Out, "Lucille: pick %s on your phone, then use your fingerprint. (%ds, or wait for the password prompt)\n",
		code, int(cfg.TTL.Seconds()))

	tick := time.NewTicker(cfg.Poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		if cfg.now().Unix() > req.Expires {
			return ErrExpired
		}
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.API+"/api/unlock/result/"+req.ID, nil)
		if err != nil {
			return err
		}
		res, err := cfg.HTTP.Do(r)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		var rr resultReply
		derr := json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&rr)
		res.Body.Close()
		if derr != nil || res.StatusCode != http.StatusOK {
			continue
		}
		if rr.State != "answered" {
			continue
		}
		// Only the first answer counts, and the helper stops listening after it.
		if err := Check(pub, req, code, Response{ID: req.ID, Chosen: rr.Chosen, Sig: rr.Sig}, cfg.now()); err != nil {
			cfg.recordFail()
			return err
		}
		cfg.clearFails()
		return nil
	}
}
