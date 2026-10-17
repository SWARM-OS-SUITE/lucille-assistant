package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Pairing lets a phone get its own permanent token without ever typing one in full: run
// `lucille-assistant pair` on the PC, it prints a 6-digit code good for one phone and one
// use, you type that into the app once, and the PC hands back a real token the app keeps
// from then on — including later over Tailscale, where retyping a 48-char token every time
// SWARM_AUTH_TOKEN rotates is miserable. Mirrors the I30LTZ node agent's own pairing flow
// (`i30ltz-node pair`), same shape, adapted for a code short enough to type by hand.
//
// The trust boundary is where the code comes from, not where it gets redeemed. Generating
// one (handlePairNew) only ever answers a loopback caller — the same physical-access gate
// `/api/pair` used to be in the USB-only design, now covering just the one step that
// actually needs it. Redeeming a code (handlePairRedeem) can come from anywhere, because
// knowing the code already proves you were at the PC when it was printed. A device that
// was never handed a code this way has no token and stays blocked, on Tailscale or not.
// Revoking a lost phone, or rotating SWARM_AUTH_TOKEN, never touches an already-paired
// device: its token is independent, stored only as a SHA-256 hash (same precaution as the
// node agent's paired.json).

// var, not const, so tests can shrink the timing without a real 10-minute wait —
// same pattern as nodes.go's sampleEvery/staleAfter/reloadEvery.
var (
	pairCodeTTL         = 10 * time.Minute
	maxPairCodeAttempts = 5
	pairLockout         = 60 * time.Second
)

const maxPairedDevices = 50

// PairManager holds the one currently-live pairing code. In memory only, like the node
// agent's own pairing state: a code is worthless after 10 minutes or a restart, and there
// is never more than one active at a time — generating a new one retires the old.
type PairManager struct {
	store *Store

	mu          sync.Mutex
	code        string
	role        string // what the device that redeems this code becomes: rolePhone | roleAgent
	createdAt   time.Time
	used        bool
	failCount   int
	lockedUntil time.Time
}

func newPairManager(store *Store) *PairManager { return &PairManager{store: store} }

// sixDigitCode returns "000000".."999999", rejection-sampled from crypto/rand so every code
// is equally likely (no modulo bias).
func sixDigitCode() (string, error) {
	const span = 1_000_000
	max := uint32(1<<32-1) - uint32(1<<32-1)%span // largest multiple of span that fits in 32 bits
	var buf [4]byte
	for {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		v := binary.BigEndian.Uint32(buf[:])
		if v < max { // max is always > 0 here: span is far smaller than 2^32
			return fmt.Sprintf("%06d", v%span), nil
		}
	}
}

// Generate retires any live code and starts a fresh one, clearing the attempt lockout
// along with it — a deliberate restart, same as unplugging and replugging would be.
func (p *PairManager) Generate() (string, error) { return p.GenerateRole(rolePhone) }

// GenerateRole is Generate for a code that pairs a device with a specific role. A code
// for Dr. Boltz's agent and a code for a phone look identical; the role is fixed here,
// on the PC, when the code is minted, never chosen by whoever redeems it.
func (p *PairManager) GenerateRole(role string) (string, error) {
	role, ok := normRole(role)
	if !ok {
		return "", errors.New(`role must be "phone" or "agent"`)
	}
	code, err := sixDigitCode()
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.code, p.role, p.createdAt, p.used, p.failCount, p.lockedUntil = code, role, time.Now(), false, 0, time.Time{}
	return code, nil
}

var errWrongOrExpiredCode = errors.New("wrong or expired code")

// Redeem checks the code, mints a permanent per-device token on success, and never leaks
// *why* a code failed (wrong digits, expired, already used, no code generated yet, and a
// locked-out caller all look identical from the outside).
func (p *PairManager) Redeem(code, label string) (id, token string, err error) {
	p.mu.Lock()
	locked := time.Now().Before(p.lockedUntil)
	if locked {
		p.mu.Unlock()
		return "", "", fmt.Errorf("too many wrong codes; try again in %s", time.Until(p.lockedUntil).Round(time.Second))
	}
	valid := p.code != "" && !p.used && time.Since(p.createdAt) < pairCodeTTL &&
		subtle.ConstantTimeCompare([]byte(p.code), []byte(code)) == 1
	if !valid {
		p.failCount++
		if p.failCount >= maxPairCodeAttempts {
			p.lockedUntil = time.Now().Add(pairLockout)
		}
		p.mu.Unlock()
		return "", "", errWrongOrExpiredCode
	}
	p.used = true // single-use, the instant it matches — a retry with the same code fails even within the TTL
	role := p.role
	p.mu.Unlock()
	return p.store.pairDeviceRole(label, role)
}

// ── Paired devices (persisted) ───────────────────────────────────

type PairedDevice struct {
	ID        string    `json:"id"` // random, not secret; how the CLI names a device to revoke
	Label     string    `json:"label"`
	Role      string    `json:"role,omitempty"` // "" (older pairings) means phone; "agent" is Dr. Boltz's worker
	TokenHash string    `json:"token_hash"`     // sha256 of the token, hex
	Paired    time.Time `json:"paired"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
}

func (s *Store) pairedPath() string { return filepath.Join(s.dir, "paired.json") }

func (s *Store) readPaired() ([]PairedDevice, error) {
	var list []PairedDevice
	err := s.readJSON(s.pairedPath(), &list)
	return list, err
}

func (s *Store) writePaired(list []PairedDevice) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	return s.writeJSON(s.pairedPath(), list)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// pairDevice mints a new device token and stores its hash. Only called after Redeem has
// already verified a valid, unused, unexpired code.
func (s *Store) pairDevice(label string) (id, token string, err error) {
	return s.pairDeviceRole(label, rolePhone)
}

func (s *Store) pairDeviceRole(label, role string) (id, token string, err error) {
	role, ok := normRole(role)
	if !ok {
		return "", "", errors.New(`role must be "phone" or "agent"`)
	}
	label = cleanLabel(label)
	if label == "" || len([]rune(label)) > 60 {
		label = role
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readPaired()
	if err != nil {
		return "", "", err
	}
	if len(list) >= maxPairedDevices {
		return "", "", errors.New("too many paired devices; revoke one first")
	}
	if token, err = randomToken(); err != nil {
		return "", "", err
	}
	if id, err = randomID(); err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	stored := role
	if role == rolePhone {
		stored = "" // keep paired.json for phones byte-compatible with older versions
	}
	list = append(list, PairedDevice{ID: id, Label: label, Role: stored, TokenHash: hashToken(token), Paired: now, LastSeen: now})
	if err := s.writePaired(list); err != nil {
		return "", "", err
	}
	return id, token, nil
}

// CheckDeviceToken reports whether got matches a paired device, touching that device's
// LastSeen on success. A constant-time compare per stored hash: the device list is small
// (dozens at most, never attacker-controlled input), so this stays cheap.
func (s *Store) CheckDeviceToken(got string) bool {
	_, ok := s.DeviceForToken(got)
	return ok
}

// DeviceForToken is CheckDeviceToken that also says which device matched, so auth() can
// tell a phone from Dr. Boltz's agent.
func (s *Store) DeviceForToken(got string) (PairedDevice, bool) {
	if got == "" {
		return PairedDevice{}, false
	}
	gotHash := []byte(hashToken(got))
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readPaired()
	if err != nil {
		return PairedDevice{}, false
	}
	matched := -1
	for i, d := range list {
		if subtle.ConstantTimeCompare([]byte(d.TokenHash), gotHash) == 1 {
			matched = i
		}
	}
	if matched < 0 {
		return PairedDevice{}, false
	}
	// Writing LastSeen on every request is wasteful for an agent that polls every few
	// seconds; once a minute is plenty for "last seen".
	if time.Since(list[matched].LastSeen) > time.Minute {
		list[matched].LastSeen = time.Now().UTC()
		_ = s.writePaired(list) // best-effort; a failed LastSeen write must never undo a successful auth
	}
	return list[matched], true
}

// cleanLabel keeps a device label to printable text. It is shown in the shared transcript
// and in a terminal, so control characters (escape sequences) must never survive.
func cleanLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

func (s *Store) ListPaired() ([]PairedDevice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readPaired()
	sort.Slice(list, func(i, j int) bool { return list[i].Paired.Before(list[j].Paired) })
	return list, err
}

// RevokePaired removes every device matching an id or an exact label (a label is not
// unique, so "my old phone" revokes all of them at once; that is the intent, not a bug).
func (s *Store) RevokePaired(idOrLabel string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.readPaired()
	if err != nil {
		return false, err
	}
	kept := list[:0]
	removed := false
	for _, d := range list {
		if d.ID == idOrLabel || d.Label == idOrLabel {
			removed = true
			continue
		}
		kept = append(kept, d)
	}
	if !removed {
		return false, nil
	}
	return true, s.writePaired(kept)
}

// ── HTTP ────────────────────────────────────────────────────────

// handlePairNew only ever answers a loopback caller, checked here directly rather than
// trusting auth()'s own loopback exemption alone: generating a code is the one action
// that must stay true "physically at the PC" even if a remote caller somehow holds the
// shared SWARM_AUTH_TOKEN — that token rotating or leaking must never let someone mint
// pairing codes for new devices from afar.
func (a *App) handlePairNew(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		writeErr(w, http.StatusForbidden, "Pairing codes can only be generated on this PC itself.")
		return
	}
	// A token must never be able to bootstrap more access. Dr. Boltz's agent in particular
	// could otherwise pair a "phone" for itself and approve its own work.
	if c := callerOf(r); c.IsAgent() || c.BadToken {
		writeErr(w, http.StatusForbidden, "Only a person at this PC can generate a pairing code.")
		return
	}
	role, ok := normRole(r.URL.Query().Get("role"))
	if !ok {
		writeErr(w, http.StatusBadRequest, `role must be "phone" or "agent".`)
		return
	}
	code, err := a.pairing.GenerateRole(role)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "code": code, "role": role, "expires_in_seconds": int(pairCodeTTL.Seconds())})
}

// handlePairRedeem is open to any caller: the code, not the network path, is the proof.
func (a *App) handlePairRedeem(w http.ResponseWriter, r *http.Request) {
	var req struct{ Code, Label string }
	if err := readJSON(r, &req); err != nil || strings.TrimSpace(req.Code) == "" {
		writeErr(w, http.StatusBadRequest, `Send {"code": "123456", "label": "..."}.`)
		return
	}
	id, token, err := a.pairing.Redeem(strings.TrimSpace(req.Code), req.Label)
	if err != nil {
		code := http.StatusUnauthorized
		if !errors.Is(err, errWrongOrExpiredCode) {
			code = http.StatusTooManyRequests
		}
		writeErr(w, code, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "id": id, "token": token})
}

// ── `lucille-assistant pair` / `devices ...` (run on this PC) ──

// loopbackAPIBase finds the port the already-running service listens on (every real
// config keeps a 127.0.0.1 entry in Listen; the port is the same on every address in it).
func loopbackAPIBase(cfg Config) string {
	port := "8403"
	for _, addr := range strings.Split(cfg.Listen, ";") {
		if _, p, err := net.SplitHostPort(strings.TrimSpace(addr)); err == nil && p != "" {
			port = p
			break
		}
	}
	return "http://127.0.0.1:" + port
}

// runPairCLI is a separate process from the running service (same as `i30ltz-node pair`
// talking to its own already-running agent over 127.0.0.1): it asks that service for a
// code over loopback HTTP rather than generating one itself, which would only ever exist
// in this short-lived command's own memory and never reach the service answering phones.
func runPairCLI(args []string, out *strings.Builder) int {
	role := rolePhone
	switch {
	case len(args) == 0:
	case len(args) == 2 && args[0] == "--role":
		role = args[1]
	default:
		fmt.Fprintln(out, "usage: lucille-assistant pair [--role phone|agent]")
		return 2
	}
	if _, ok := normRole(role); !ok {
		fmt.Fprintln(out, `error: role must be "phone" or "agent"`)
		return 2
	}
	base := loopbackAPIBase(loadConfig())
	resp, err := http.Get(base + "/api/pair/new?role=" + role)
	if err != nil {
		fmt.Fprintf(out, "error: could not reach the running service at %s: %v\n", base, err)
		fmt.Fprintln(out, "Is the service running? Try: systemctl --user status lucille-assistant")
		return 1
	}
	defer resp.Body.Close()
	var r struct {
		OK               bool   `json:"ok"`
		Code             string `json:"code"`
		ExpiresInSeconds int    `json:"expires_in_seconds"`
		Error            string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		fmt.Fprintln(out, "error: could not read the service's reply:", err)
		return 1
	}
	if !r.OK {
		msg := r.Error
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		fmt.Fprintln(out, "error:", msg)
		return 1
	}
	fmt.Fprintf(out, "Pairing code: %s   (valid %d min, works once, pairs a device with role: %s)\n", r.Code, r.ExpiresInSeconds/60, role)
	if role == roleAgent {
		fmt.Fprintln(out, "For Dr. Boltz's worker:  python3 boltz_bridge.py pair "+r.Code)
	} else {
		fmt.Fprintln(out, "In the app: Settings -> Pair with this PC -> enter this code.")
	}
	return 0
}

func runDevicesCLI(args []string, out *strings.Builder) int {
	cfg := loadConfig()
	st, err := openStore(cfg.DataDir)
	if err != nil {
		fmt.Fprintln(out, "error:", err)
		return 1
	}
	switch {
	case len(args) == 1 && args[0] == "list":
		list, err := st.ListPaired()
		if err != nil {
			fmt.Fprintln(out, "error:", err)
			return 1
		}
		if len(list) == 0 {
			fmt.Fprintln(out, "no devices paired yet — run `lucille-assistant pair` and enter the code in the app")
		}
		for _, d := range list {
			seen := "never"
			if !d.LastSeen.IsZero() {
				seen = d.LastSeen.Format("2006-01-02 15:04")
			}
			role, _ := normRole(d.Role)
			fmt.Fprintf(out, "%-14s %-6s %-30s paired %s  last seen %s\n", d.ID, role, d.Label, d.Paired.Format("2006-01-02 15:04"), seen)
		}
		return 0
	case len(args) == 2 && args[0] == "revoke":
		ok, err := st.RevokePaired(args[1])
		if err != nil {
			fmt.Fprintln(out, "error:", err)
			return 1
		}
		if !ok {
			fmt.Fprintf(out, "no paired device matched %q\n", args[1])
			return 1
		}
		fmt.Fprintf(out, "revoked %q; that phone will need to pair again\n", args[1])
		return 0
	default:
		fmt.Fprintln(out, "usage: lucille-assistant devices list | revoke <id-or-label>")
		return 2
	}
}
