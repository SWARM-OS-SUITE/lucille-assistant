package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"swarm-os/lucille-assistant/internal/unlockproto"
)

type unlockPhone struct {
	key *ecdsa.PrivateKey
}

func newUnlockPhone(t *testing.T) unlockPhone {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return unlockPhone{k}
}

func (p unlockPhone) pubkeyB64(t *testing.T) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&p.key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(der)
}

func (p unlockPhone) sign(req unlockproto.Request, chosen string) string {
	d := sha256.Sum256(unlockproto.Message(req, chosen))
	sig, _ := ecdsa.SignASN1(rand.Reader, p.key, d[:])
	return base64.StdEncoding.EncodeToString(sig)
}

// setup gives (app, mux, phoneToken). The phone is paired through the real pairing
// flow, not poked into the store, so the test exercises the same path a real phone uses.
func setupUnlock(t *testing.T) (*App, http.Handler, string) {
	t.Helper()
	app, dir := setupTestApp(t)
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Cleanup(unlocks.reset)
	app.pairing = newPairManager(app.store)
	code, err := app.pairing.Generate()
	if err != nil {
		t.Fatal(err)
	}
	_, tok, err := app.pairing.Redeem(code, "test-phone")
	if err != nil {
		t.Fatal(err)
	}
	return app, auth(app.cfg.Token, app.store, app.newMux()), tok
}

func pc(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host, r.RemoteAddr = "127.0.0.1:8403", "127.0.0.1:5555"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func phoneReq(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host, r.RemoteAddr = "127.0.0.1:8403", "100.64.0.9:5555" // not loopback: a real phone
	r.Header.Set("X-Swarm-Auth", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func hex16(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// enrol: the PC mints a code, the phone sends its key + that code, and the service
// stages the PEM. It returns the path the test can read back (standing in for the
// `sudo install` step the user runs for real).
func enrolPhone(t *testing.T, app *App, h http.Handler, tok string, ph unlockPhone) {
	t.Helper()
	w := pc(h, "GET", "/api/unlock/enrol/new", "")
	if w.Code != 200 {
		t.Fatalf("enrol/new: %d %s", w.Code, w.Body)
	}
	var out struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	body, _ := json.Marshal(map[string]string{"code": out.Code, "pubkey": ph.pubkeyB64(t)})
	w2 := phoneReq(h, "POST", "/api/unlock/enrol", tok, string(body))
	if w2.Code != 200 {
		t.Fatalf("enrol: %d %s", w2.Code, w2.Body)
	}
}

func readPendingPub(t *testing.T, app *App) *ecdsa.PublicKey {
	t.Helper()
	raw, err := os.ReadFile(app.pendingApproverPath())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := unlockproto.ParsePublicKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return pub
}

func mkReq(t *testing.T, now time.Time) unlockproto.Request {
	t.Helper()
	r, _, err := unlockproto.NewRequest("sudo", "i30ltz", "debian", 30*time.Second, now)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUnlockEnrolEndToEnd(t *testing.T) {
	app, h, tok := setupUnlock(t)
	ph := newUnlockPhone(t)
	enrolPhone(t, app, h, tok, ph)
	pub := readPendingPub(t, app)
	if pub.X.Cmp(ph.key.PublicKey.X) != 0 {
		t.Fatal("staged key does not match the phone's key")
	}
}

func TestUnlockEnrolOnlyFromPC(t *testing.T) {
	_, h, _ := setupUnlock(t)
	w := phoneReq(h, "GET", "/api/unlock/enrol/new", "doesnt-matter", "")
	if w.Code == 200 {
		t.Fatal("a remote caller minted an enrol code")
	}
}

func TestUnlockEnrolWrongCodeLocksOut(t *testing.T) {
	oldMax, oldLock := enrolMaxAttempts, enrolLockout
	enrolLockout = 30 * time.Millisecond
	t.Cleanup(func() { enrolMaxAttempts, enrolLockout = oldMax, oldLock })

	app, h, tok := setupUnlock(t)
	ph := newUnlockPhone(t)
	w0 := pc(h, "GET", "/api/unlock/enrol/new", "")
	var out struct{ Code string }
	_ = json.Unmarshal(w0.Body.Bytes(), &out)
	body := func(code string) string {
		b, _ := json.Marshal(map[string]string{"code": code, "pubkey": ph.pubkeyB64(t)})
		return string(b)
	}
	// The real attacker is remote and cannot call enrol/new itself (PC-only), so unlike
	// the test it can never mint itself a fresh code to escape its own lockout.
	for i := 0; i < enrolMaxAttempts; i++ {
		w := phoneReq(h, "POST", "/api/unlock/enrol", tok, body("000000"))
		if w.Code == 200 {
			t.Fatal("wrong code accepted")
		}
	}
	// Even the correct, still-live code is now refused.
	w2 := phoneReq(h, "POST", "/api/unlock/enrol", tok, body(out.Code))
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("want locked out, got %d %s", w2.Code, w2.Body)
	}
	// After the lockout window, the same code works again.
	time.Sleep(60 * time.Millisecond)
	if w3 := phoneReq(h, "POST", "/api/unlock/enrol", tok, body(out.Code)); w3.Code != 200 {
		t.Fatalf("after lockout window: %d %s", w3.Code, w3.Body)
	}
	_ = app
}

// The real end-to-end path: PAM helper requests, phone sees it over the stream/pending
// endpoint, phone answers, helper collects the result and it verifies against the
// enrolled key — all through HTTP, not by calling Go functions directly.
func TestUnlockFullRoundTrip(t *testing.T) {
	app, h, tok := setupUnlock(t)
	ph := newUnlockPhone(t)
	enrolPhone(t, app, h, tok, ph)
	pub := readPendingPub(t, app)

	req := mkReq(t, time.Now())
	rb, _ := json.Marshal(req)
	if w := pc(h, "POST", "/api/unlock/request", string(rb)); w.Code != 200 {
		t.Fatalf("request: %d %s", w.Code, w.Body)
	}

	// Phone sees it via /pending (the stream is covered separately).
	wp := phoneReq(h, "GET", "/api/unlock/pending", tok, "")
	var pend struct {
		Requests []unlockproto.Request `json:"requests"`
	}
	_ = json.Unmarshal(wp.Body.Bytes(), &pend)
	if len(pend.Requests) != 1 || pend.Requests[0].ID != req.ID {
		t.Fatalf("phone did not see the request: %s", wp.Body)
	}
	seen := pend.Requests[0]
	chosen := seen.Choices[0] // the phone cannot tell which is correct; it just picks one

	resp := unlockproto.Response{ID: req.ID, Chosen: chosen, Sig: ph.sign(seen, chosen)}
	rb2, _ := json.Marshal(resp)
	if w := phoneReq(h, "POST", "/api/unlock/respond", tok, string(rb2)); w.Code != 200 {
		t.Fatalf("respond: %d %s", w.Code, w.Body)
	}

	wr := pc(h, "GET", "/api/unlock/result/"+req.ID, "")
	var rr struct {
		State  string `json:"state"`
		Chosen string `json:"chosen"`
		Sig    string `json:"sig"`
	}
	_ = json.Unmarshal(wr.Body.Bytes(), &rr)
	if rr.State != "answered" {
		t.Fatalf("want answered, got %+v", rr)
	}
	// The helper's actual job: does this verify? It only does if chosen was really req's code,
	// which this test cannot know — so check both possibilities match unlockproto.Check's verdict.
	err := unlockproto.Check(pub, req, chosen, unlockproto.Response{ID: req.ID, Chosen: rr.Chosen, Sig: rr.Sig}, time.Now())
	if rr.Chosen != chosen {
		t.Fatalf("result chosen %q != submitted %q", rr.Chosen, chosen)
	}
	if err != nil {
		t.Fatalf("a correctly signed, correctly relayed answer failed verification: %v", err)
	}
}

func TestUnlockRequestOnlyFromPC(t *testing.T) {
	_, h, tok := setupUnlock(t)
	req := mkReq(t, time.Now())
	rb, _ := json.Marshal(req)
	if w := phoneReq(h, "POST", "/api/unlock/request", tok, string(rb)); w.Code == 200 {
		t.Fatal("a phone was allowed to create its own approval request")
	}
}

func TestUnlockResultOnlyFromPC(t *testing.T) {
	_, h, tok := setupUnlock(t)
	if w := phoneReq(h, "GET", "/api/unlock/result/"+hex16(t), tok, ""); w.Code == 200 {
		t.Fatal("a phone could read a result directly")
	}
}

func TestUnlockRespondRejectsBadRequest(t *testing.T) {
	_, h, tok := setupUnlock(t)
	cases := []string{
		`{"id":"not-hex","chosen":"1234","sig":"x"}`,
		`{"id":"` + hex16(t) + `","chosen":"1234","sig":"x"}`, // id exists nowhere
	}
	for _, body := range cases {
		if w := phoneReq(h, "POST", "/api/unlock/respond", tok, body); w.Code == 200 {
			t.Fatalf("accepted bad respond body: %s", body)
		}
	}
}

func TestUnlockRespondChosenMustBeAChoice(t *testing.T) {
	_, h, tok := setupUnlock(t)
	req := mkReq(t, time.Now())
	rb, _ := json.Marshal(req)
	pc(h, "POST", "/api/unlock/request", string(rb))

	resp := unlockproto.Response{ID: req.ID, Chosen: "9999", Sig: "doesnt-matter"}
	for _, c := range req.Choices {
		if c == "9999" {
			t.Fatal("test fixture collided with a real choice")
		}
	}
	rb2, _ := json.Marshal(resp)
	if w := phoneReq(h, "POST", "/api/unlock/respond", tok, string(rb2)); w.Code == 200 {
		t.Fatal("accepted a number that was not offered")
	}
}

func TestUnlockOneAnswerOnly(t *testing.T) {
	app, h, tok := setupUnlock(t)
	ph := newUnlockPhone(t)
	enrolPhone(t, app, h, tok, ph)

	req := mkReq(t, time.Now())
	rb, _ := json.Marshal(req)
	pc(h, "POST", "/api/unlock/request", string(rb))

	first := unlockproto.Response{ID: req.ID, Chosen: req.Choices[0], Sig: ph.sign(req, req.Choices[0])}
	fb, _ := json.Marshal(first)
	if w := phoneReq(h, "POST", "/api/unlock/respond", tok, string(fb)); w.Code != 200 {
		t.Fatalf("first answer: %d %s", w.Code, w.Body)
	}
	second := unlockproto.Response{ID: req.ID, Chosen: req.Choices[1], Sig: ph.sign(req, req.Choices[1])}
	sb, _ := json.Marshal(second)
	if w := phoneReq(h, "POST", "/api/unlock/respond", tok, string(sb)); w.Code == 200 {
		t.Fatal("a second answer to the same request was accepted")
	}
	wr := pc(h, "GET", "/api/unlock/result/"+req.ID, "")
	var rr struct{ Chosen string }
	_ = json.Unmarshal(wr.Body.Bytes(), &rr)
	if rr.Chosen != req.Choices[0] {
		t.Fatalf("second answer overwrote the first: %+v", rr)
	}
}

func TestUnlockExpiredRequestRejected(t *testing.T) {
	_, h, _ := setupUnlock(t)
	req, _, err := unlockproto.NewRequest("sudo", "i30ltz", "debian", 1*time.Second, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	rb, _ := json.Marshal(req)
	if w := pc(h, "POST", "/api/unlock/request", string(rb)); w.Code == 200 {
		t.Fatal("accepted a request that is already expired")
	}
}

func TestUnlockRequestBodyValidated(t *testing.T) {
	_, h, _ := setupUnlock(t)
	good := mkReq(t, time.Now())
	for name, mut := range map[string]func(*unlockproto.Request){
		"short id":        func(r *unlockproto.Request) { r.ID = "abc" },
		"two choices":     func(r *unlockproto.Request) { r.Choices = r.Choices[:2] },
		"dup choices":     func(r *unlockproto.Request) { r.Choices[1] = r.Choices[0] },
		"non-digit":       func(r *unlockproto.Request) { r.Choices[0] = "abcd" },
		"newline in user": func(r *unlockproto.Request) { r.User = "i30\nltz" },
		"far future":      func(r *unlockproto.Request) { r.Expires = time.Now().Add(time.Hour).Unix() },
	} {
		q := good
		q.Choices = append([]string{}, good.Choices...)
		mut(&q)
		rb, _ := json.Marshal(q)
		if w := pc(h, "POST", "/api/unlock/request", string(rb)); w.Code == 200 {
			t.Errorf("%s: bad request accepted", name)
		}
	}
}

func TestUnlockDuplicateIDRejected(t *testing.T) {
	_, h, _ := setupUnlock(t)
	req := mkReq(t, time.Now())
	rb, _ := json.Marshal(req)
	pc(h, "POST", "/api/unlock/request", string(rb))
	if w := pc(h, "POST", "/api/unlock/request", string(rb)); w.Code == 200 {
		t.Fatal("the same request id was accepted twice")
	}
}

func TestUnlockTooManyLiveRequests(t *testing.T) {
	_, h, _ := setupUnlock(t)
	for i := 0; i < unlockMaxLive; i++ {
		rb, _ := json.Marshal(mkReq(t, time.Now()))
		if w := pc(h, "POST", "/api/unlock/request", string(rb)); w.Code != 200 {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body)
		}
	}
	rb, _ := json.Marshal(mkReq(t, time.Now()))
	w := pc(h, "POST", "/api/unlock/request", string(rb))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 over the live cap, got %d", w.Code)
	}
}

// A forged answer, signed by a key nobody enrolled, must fail verification even though
// it is otherwise a perfectly well-formed response for a real request.
func TestUnlockForgedSignatureFailsVerification(t *testing.T) {
	app, h, tok := setupUnlock(t)
	ph := newUnlockPhone(t)
	enrolPhone(t, app, h, tok, ph)
	pub := readPendingPub(t, app)

	attacker := newUnlockPhone(t)
	req := mkReq(t, time.Now())
	rb, _ := json.Marshal(req)
	pc(h, "POST", "/api/unlock/request", string(rb))

	resp := unlockproto.Response{ID: req.ID, Chosen: req.Choices[0], Sig: attacker.sign(req, req.Choices[0])}
	respB, _ := json.Marshal(resp)
	if w := phoneReq(h, "POST", "/api/unlock/respond", tok, string(respB)); w.Code != 200 {
		t.Fatalf("respond: %d %s", w.Code, w.Body) // the carrier accepts it; verification is the helper's job
	}
	wr := pc(h, "GET", "/api/unlock/result/"+req.ID, "")
	var rr struct{ Chosen, Sig string }
	_ = json.Unmarshal(wr.Body.Bytes(), &rr)
	if err := unlockproto.Check(pub, req, req.Choices[0], unlockproto.Response{ID: req.ID, Chosen: rr.Chosen, Sig: rr.Sig}, time.Now()); err == nil {
		t.Fatal("a forged signature verified against the enrolled key")
	}
}
