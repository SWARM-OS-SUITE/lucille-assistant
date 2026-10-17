package main

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Who is calling. auth() decides this once per request and the handlers that care
// (the Lucille <-> Dr. Boltz bridge, PC actions) read it back with callerOf.
//
// The rule that matters: Dr. Boltz is a machine, so it gets its own role. A paired device
// with role "agent" can do the agent's half of the bridge (claim a challenge, report
// progress, ask for an action) and nothing a person decides (approve work, approve root,
// start a challenge). An agent's token identifies it even from loopback, so being on the
// same PC does not turn it into "the person at the PC".
//
// Known limit, same as the loopback trust this service always had: a caller on loopback
// that sends no token is treated as the person at this PC. A process running as your own
// user can do that. Dr. Boltz's worker therefore runs its coding engine in a sandbox with
// no route to this service (see boltz_bridge.py), and the README spells out the hardening
// step for anyone who wants a hard boundary (run the worker as a separate user).
type callerKind string

const (
	callerPC    callerKind = "pc"    // loopback, no token: the person at this PC
	callerMain  callerKind = "main"  // presented SWARM_AUTH_TOKEN
	callerPhone callerKind = "phone" // paired device, role "phone"
	callerAgent callerKind = "agent" // paired device, role "agent" (Dr. Boltz)
	callerOpen  callerKind = "open"  // remote caller on a service that has no token configured
)

type Caller struct {
	Kind     callerKind
	DeviceID string
	Label    string
	// BadToken is set when a token was presented but matched nothing. On loopback the
	// request is still served (old behaviour), but the bridge refuses to treat it as a
	// person, so a revoked agent token never falls back to "the person at the PC".
	BadToken bool
}

func (c Caller) IsAgent() bool { return c.Kind == callerAgent }

// IsHuman means a person is deciding: not the agent, and not a caller whose token failed.
func (c Caller) IsHuman() bool { return c.Kind != callerAgent && !c.BadToken }

// Name is how the caller is shown in the shared transcript.
func (c Caller) Name() string {
	switch c.Kind {
	case callerAgent:
		return "boltz"
	case callerPhone:
		if c.Label != "" {
			return c.Label
		}
		return "phone"
	}
	return "you"
}

// Device roles. A device paired before roles existed has none and is a phone.
const (
	rolePhone = "phone"
	roleAgent = "agent"
)

func normRole(r string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "", rolePhone:
		return rolePhone, true
	case roleAgent:
		return roleAgent, true
	}
	return "", false
}

func callerForDevice(d PairedDevice) Caller {
	kind := callerPhone
	if d.Role == roleAgent {
		kind = callerAgent
	}
	return Caller{Kind: kind, DeviceID: d.ID, Label: d.Label}
}

type callerCtxKey struct{}

func withCaller(r *http.Request, c Caller) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), callerCtxKey{}, c))
}

// callerOf returns who auth() decided is calling. A request that never went through
// auth() (a handler called directly) is the person at the PC, matching loopback.
func callerOf(r *http.Request) Caller {
	if c, ok := r.Context().Value(callerCtxKey{}).(Caller); ok {
		return c
	}
	return Caller{Kind: callerPC}
}

// ── Cross-site guard ────────────────────────────────────────────
//
// Loopback callers need no token, and every response carries `Access-Control-Allow-Origin: *`.
// Together that means any web page open in a browser on this PC could drive the endpoints
// that matter most (queue a task for the coding agent, run a PC action, mint a pairing
// code) just by fetch()-ing http://127.0.0.1:8403. browserSafe closes that for the
// powerful routes: the request must be same-origin (or carry no Origin at all, which is
// what the phone app, curl and the CLI send) and its Host header must be an address, not a
// DNS name an attacker controls (DNS rebinding).

func hostAllowed(hostport string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	h = strings.ToLower(strings.Trim(h, "[]"))
	switch {
	case h == "":
		return false
	case h == "localhost", strings.HasSuffix(h, ".localhost"):
		return true
	case net.ParseIP(h) != nil:
		return true
	case !strings.Contains(h, "."): // short tailnet/MagicDNS names
		return true
	case strings.HasSuffix(h, ".ts.net"):
		return true
	}
	return false
}

func sameOrigin(r *http.Request) bool {
	if !hostAllowed(r.Host) {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
	default: // cross-site, same-site (another port or another local site)
		return false
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" {
		return false // "null" and anything unparseable
	}
	return strings.EqualFold(u.Host, r.Host)
}

func browserSafe(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			writeErr(w, http.StatusForbidden, "Cross-site requests are not allowed here.")
			return
		}
		h(w, r)
	}
}

// requireHuman / requireAgent are the two gates every bridge handler starts with.
func requireHuman(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	c := callerOf(r)
	switch {
	case c.Kind == callerOpen:
		writeErr(w, http.StatusUnauthorized, "The bridge needs this phone to be paired, or the main token.")
		return c, false
	case c.BadToken:
		writeErr(w, http.StatusUnauthorized, "That token is no longer valid. Pair this device again.")
		return c, false
	case c.IsAgent():
		writeErr(w, http.StatusForbidden, "Only a person can do that, not Dr. Boltz.")
		return c, false
	}
	return c, true
}

func requireAgent(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	c := callerOf(r)
	if !c.IsAgent() || c.BadToken {
		writeErr(w, http.StatusForbidden, "Only Dr. Boltz's paired agent can do that (lucille-assistant pair --role agent).")
		return c, false
	}
	return c, true
}

// requireKnown lets any authenticated caller, person or agent, through.
func requireKnown(w http.ResponseWriter, r *http.Request) (Caller, bool) {
	c := callerOf(r)
	if c.Kind == callerOpen {
		writeErr(w, http.StatusUnauthorized, "The bridge needs this phone to be paired, or the main token.")
		return c, false
	}
	if c.BadToken {
		writeErr(w, http.StatusUnauthorized, "That token is no longer valid. Pair this device again.")
		return c, false
	}
	return c, true
}
