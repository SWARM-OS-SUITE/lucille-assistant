package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var sixDigits = regexp.MustCompile(`^\d{6}$`)

func fastPairing(t *testing.T) (*App, *Store) {
	t.Helper()
	app, dir := setupTestApp(t)
	t.Cleanup(func() { os.RemoveAll(dir) })
	app.pairing = newPairManager(app.store)
	oldTTL, oldMax, oldLockout := pairCodeTTL, maxPairCodeAttempts, pairLockout
	pairCodeTTL, pairLockout = 200*time.Millisecond, 150*time.Millisecond
	t.Cleanup(func() { pairCodeTTL, maxPairCodeAttempts, pairLockout = oldTTL, oldMax, oldLockout })
	return app, app.store
}

func TestSixDigitCodeIsUniformAndRight(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		c, err := sixDigitCode()
		if err != nil {
			t.Fatal(err)
		}
		if !sixDigits.MatchString(c) {
			t.Fatalf("code %q is not 6 digits", c)
		}
		seen[c] = true
	}
	if len(seen) < 480 { // 500 draws from a 1e6 space colliding this much would be exceptional
		t.Errorf("only %d distinct codes out of 500 draws, looks biased", len(seen))
	}
}

func TestPairManagerHappyPath(t *testing.T) {
	_, st := fastPairing(t)
	pm := newPairManager(st)
	code, err := pm.Generate()
	if err != nil || !sixDigits.MatchString(code) {
		t.Fatalf("code=%q err=%v", code, err)
	}
	id, token, err := pm.Redeem(code, "Test Phone")
	if err != nil || id == "" || len(token) != 64 { // 32 bytes hex-encoded
		t.Fatalf("id=%q token_len=%d err=%v", id, len(token), err)
	}
	if !st.CheckDeviceToken(token) {
		t.Fatal("the minted token does not authenticate against the store")
	}
	list, err := st.ListPaired()
	if err != nil || len(list) != 1 || list[0].ID != id || list[0].Label != "Test Phone" || list[0].TokenHash == token {
		t.Fatalf("list=%+v err=%v (token_hash must never equal the raw token)", list, err)
	}
}

func TestPairManagerRejectsWrongExpiredAndReusedCodes(t *testing.T) {
	_, st := fastPairing(t)
	pm := newPairManager(st)

	// No code generated yet.
	if _, _, err := pm.Redeem("123456", "x"); err != errWrongOrExpiredCode {
		t.Fatalf("no code yet: err=%v", err)
	}

	code, _ := pm.Generate()
	if _, _, err := pm.Redeem("000000", "x"); code == "000000" || err != errWrongOrExpiredCode {
		t.Fatalf("wrong code: err=%v", err)
	}

	// Single-use: redeeming the right code twice fails the second time.
	if _, _, err := pm.Redeem(code, "first"); err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if _, _, err := pm.Redeem(code, "second"); err != errWrongOrExpiredCode {
		t.Fatalf("reused code should fail, got err=%v", err)
	}

	// Expiry.
	code2, _ := pm.Generate()
	time.Sleep(pairCodeTTL + 50*time.Millisecond)
	if _, _, err := pm.Redeem(code2, "x"); err != errWrongOrExpiredCode {
		t.Fatalf("expired code: err=%v", err)
	}
}

func TestPairManagerLocksOutAfterRepeatedWrongCodes(t *testing.T) {
	_, st := fastPairing(t)
	pm := newPairManager(st)
	code, _ := pm.Generate()
	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}
	for i := 0; i < maxPairCodeAttempts; i++ {
		if _, _, err := pm.Redeem(wrong, "x"); err != errWrongOrExpiredCode {
			t.Fatalf("attempt %d: err=%v", i, err)
		}
	}
	// Now locked out — even the correct code is refused, and the message is not the
	// generic wrong-code error (the caller needs to know to back off, not retype).
	if _, _, err := pm.Redeem(code, "x"); err == nil || err == errWrongOrExpiredCode {
		t.Fatalf("expected a lockout error distinct from wrong-code, got %v", err)
	}
	time.Sleep(pairLockout + 50*time.Millisecond)
	code2, _ := pm.Generate() // Generate clears the lockout, same as unplugging and replugging
	if _, _, err := pm.Redeem(code2, "x"); err != nil {
		t.Fatalf("after lockout expires and a fresh code: %v", err)
	}
}

func TestPairManagerIsConcurrencySafe(t *testing.T) {
	_, st := fastPairing(t)
	pm := newPairManager(st)
	code, _ := pm.Generate()
	var wg sync.WaitGroup
	oks := 0
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := pm.Redeem(code, "racer"); err == nil {
				mu.Lock()
				oks++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if oks != 1 {
		t.Fatalf("exactly one concurrent redeemer should win a single-use code, got %d", oks)
	}
}

func TestLoopbackAPIBaseParsesPort(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1:8403":                  "http://127.0.0.1:8403",
		"100.82.1.19:8403;127.0.0.1:8403": "http://127.0.0.1:8403",
		"127.0.0.1:9999; 100.1.1.1:9999":  "http://127.0.0.1:9999",
		"":                                "http://127.0.0.1:8403",
	}
	for listen, want := range cases {
		if got := loopbackAPIBase(Config{Listen: listen}); got != want {
			t.Errorf("Listen=%q: got %q, want %q", listen, got, want)
		}
	}
}

// ── HTTP ────────────────────────────────────────────────────────

func TestHandlePairNewIsLoopbackOnly(t *testing.T) {
	app, _ := fastPairing(t)
	req := httptest.NewRequest("GET", "/api/pair/new", nil)
	req.RemoteAddr = "100.82.1.25:54321"
	rr := httptest.NewRecorder()
	app.handlePairNew(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("remote caller: code=%d body=%s", rr.Code, rr.Body.String())
	}

	req = httptest.NewRequest("GET", "/api/pair/new", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rr = httptest.NewRecorder()
	app.handlePairNew(rr, req)
	var out struct {
		OK   bool   `json:"ok"`
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if rr.Code != http.StatusOK || !out.OK || !sixDigits.MatchString(out.Code) {
		t.Fatalf("loopback caller: code=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandlePairRedeemEndToEnd(t *testing.T) {
	app, _ := fastPairing(t)
	newReq := httptest.NewRequest("GET", "/api/pair/new", nil)
	newReq.RemoteAddr = "127.0.0.1:1"
	rr := httptest.NewRecorder()
	app.handlePairNew(rr, newReq)
	var gen struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &gen)

	// Redeeming works from a non-loopback address: the code is the proof, not the network path.
	body, _ := json.Marshal(map[string]string{"code": gen.Code, "label": "My Phone"})
	req := httptest.NewRequest("POST", "/api/pair", bytes.NewReader(body))
	req.RemoteAddr = "100.82.1.25:54321"
	rr = httptest.NewRecorder()
	app.handlePairRedeem(rr, req)
	var out struct {
		OK    bool   `json:"ok"`
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if rr.Code != http.StatusOK || !out.OK || out.Token == "" {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}

	// A bogus code, same request shape, is refused.
	body2, _ := json.Marshal(map[string]string{"code": "000000"})
	req2 := httptest.NewRequest("POST", "/api/pair", bytes.NewReader(body2))
	req2.RemoteAddr = "100.82.1.25:54321"
	rr2 := httptest.NewRecorder()
	app.handlePairRedeem(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("bogus code: code=%d body=%s", rr2.Code, rr2.Body.String())
	}

	// Malformed body.
	req3 := httptest.NewRequest("POST", "/api/pair", strings.NewReader("not json"))
	req3.RemoteAddr = "100.82.1.25:54321"
	rr3 := httptest.NewRecorder()
	app.handlePairRedeem(rr3, req3)
	if rr3.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: code=%d", rr3.Code)
	}
}

// ── Devices store ───────────────────────────────────────────────

func TestDeviceTokensAndRevoke(t *testing.T) {
	app, st := fastPairing(t)
	_ = app
	id1, tok1, err := st.pairDevice("Phone A")
	if err != nil {
		t.Fatal(err)
	}
	id2, tok2, err := st.pairDevice("Phone B")
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 || tok1 == tok2 {
		t.Fatalf("two pairings produced colliding id/token: %q/%q vs %q/%q", id1, tok1, id2, tok2)
	}
	if !st.CheckDeviceToken(tok1) || !st.CheckDeviceToken(tok2) {
		t.Fatal("both real device tokens should authenticate")
	}
	if st.CheckDeviceToken("not-a-real-token") || st.CheckDeviceToken("") {
		t.Fatal("a random or empty string must never authenticate")
	}

	list, _ := st.ListPaired()
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}

	ok, err := st.RevokePaired("Phone A")
	if err != nil || !ok {
		t.Fatalf("revoke: ok=%v err=%v", ok, err)
	}
	if st.CheckDeviceToken(tok1) {
		t.Fatal("a revoked token must stop working immediately")
	}
	if !st.CheckDeviceToken(tok2) {
		t.Fatal("revoking one device must not affect another")
	}
	if ok, _ := st.RevokePaired("no such device"); ok {
		t.Fatal("revoking a name that does not exist should report false")
	}
}

func TestPairDeviceRejectsAnOverlongLabelByFallingBackSafely(t *testing.T) {
	_, st := fastPairing(t)
	id, _, err := st.pairDevice(strings.Repeat("x", 500))
	if err != nil {
		t.Fatal(err)
	}
	list, _ := st.ListPaired()
	var got string
	for _, d := range list {
		if d.ID == id {
			got = d.Label
		}
	}
	if got != "phone" {
		t.Fatalf("overlong label should fall back to %q, got %q", "phone", got)
	}
}

// ── Integration with the main auth() middleware ──────────────────

func TestAuthAcceptsAPairedDeviceToken(t *testing.T) {
	_, st := fastPairing(t)
	_, token, err := st.pairDevice("My Phone")
	if err != nil {
		t.Fatal(err)
	}
	handler := auth("master-token-xyz", st, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/api/care", nil)
	req.RemoteAddr = "100.82.1.25:54321"
	req.Header.Set("X-Swarm-Auth", token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("a paired device's own token should work, got %d", rr.Code)
	}

	req2 := httptest.NewRequest("GET", "/api/care", nil)
	req2.RemoteAddr = "100.82.1.25:54321"
	req2.Header.Set("X-Swarm-Auth", "some-other-random-string")
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("an unrelated string must still be rejected, got %d", rr2.Code)
	}
}

func TestAuthLetsAnUnauthenticatedCallerReachPairRedeem(t *testing.T) {
	_, st := fastPairing(t)
	handler := auth("master-token-xyz", st, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // distinct marker: proves next.ServeHTTP was reached
	}))
	req := httptest.NewRequest("POST", "/api/pair", bytes.NewReader([]byte(`{"code":"000000"}`)))
	req.RemoteAddr = "100.82.1.25:54321" // no token header at all
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTeapot {
		t.Fatalf("/api/pair must be reachable with no token, got %d", rr.Code)
	}
}
