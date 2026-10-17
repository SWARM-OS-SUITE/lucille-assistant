package unlockproto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// phone plays the Android side: a P-256 key that signs what it is told to.
type phone struct{ key *ecdsa.PrivateKey }

func newPhone(t *testing.T) phone {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return phone{k}
}

func (p phone) pem(t *testing.T) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&p.key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	out, err := PublicKeyPEM(der)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (p phone) answer(t *testing.T, r Request, chosen string) Response {
	t.Helper()
	d := sha256.Sum256(Message(r, chosen))
	sig, err := ecdsa.SignASN1(rand.Reader, p.key, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return Response{ID: r.ID, Chosen: chosen, Sig: base64.StdEncoding.EncodeToString(sig)}
}

var t0 = time.Unix(1_800_000_000, 0)

func newReq(t *testing.T) (Request, string) {
	t.Helper()
	r, code, err := NewRequest("sudo", "i30ltz", "debian", 45*time.Second, t0)
	if err != nil {
		t.Fatal(err)
	}
	return r, code
}

func TestRequestShape(t *testing.T) {
	for i := 0; i < 200; i++ {
		r, code := newReq(t)
		if len(r.Choices) != 3 {
			t.Fatalf("want 3 choices, got %v", r.Choices)
		}
		seen := map[string]bool{}
		found := false
		for _, c := range r.Choices {
			if len(c) != 4 || seen[c] {
				t.Fatalf("bad or duplicate choice in %v", r.Choices)
			}
			seen[c] = true
			found = found || c == code
		}
		if !found {
			t.Fatalf("code %s missing from choices %v", code, r.Choices)
		}
	}
}

// The service is untrusted, so the wire request must not reveal which number is the code.
func TestRequestDoesNotExposeCode(t *testing.T) {
	r, code := newReq(t)
	raw, _ := json.Marshal(r)
	if strings.Count(string(raw), code) != 1 {
		t.Fatalf("code should appear exactly once (as a choice), got: %s", raw)
	}
	if strings.Contains(string(raw), `"code"`) {
		t.Fatalf("request has a code field: %s", raw)
	}
}

func TestCodePositionIsNotFixed(t *testing.T) {
	pos := map[int]int{}
	for i := 0; i < 300; i++ {
		r, code := newReq(t)
		for j, c := range r.Choices {
			if c == code {
				pos[j]++
			}
		}
	}
	for j := 0; j < 3; j++ {
		if pos[j] < 40 {
			t.Fatalf("code almost never at position %d: %v", j, pos)
		}
	}
}

func TestRejectsNewlineFields(t *testing.T) {
	for _, bad := range [][3]string{{"su\ndo", "u", "h"}, {"sudo", "u\r", "h"}, {"sudo", "u", ""}} {
		if _, _, err := NewRequest(bad[0], bad[1], bad[2], time.Minute, t0); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestGoodApproval(t *testing.T) {
	p := newPhone(t)
	pub, err := ParsePublicKey(p.pem(t))
	if err != nil {
		t.Fatal(err)
	}
	r, code := newReq(t)
	if err := Check(pub, r, code, p.answer(t, r, code), t0.Add(5*time.Second)); err != nil {
		t.Fatalf("good approval rejected: %v", err)
	}
}

func TestExpired(t *testing.T) {
	p := newPhone(t)
	pub, _ := ParsePublicKey(p.pem(t))
	r, code := newReq(t)
	err := Check(pub, r, code, p.answer(t, r, code), t0.Add(46*time.Second))
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

func TestWrongNumber(t *testing.T) {
	p := newPhone(t)
	pub, _ := ParsePublicKey(p.pem(t))
	r, code := newReq(t)
	var decoy string
	for _, c := range r.Choices {
		if c != code {
			decoy = c
		}
	}
	err := Check(pub, r, code, p.answer(t, r, decoy), t0.Add(time.Second))
	if !errors.Is(err, ErrWrongNumber) {
		t.Fatalf("want ErrWrongNumber, got %v", err)
	}
}

func TestWrongKeyRejected(t *testing.T) {
	enrolled := newPhone(t)
	attacker := newPhone(t)
	pub, _ := ParsePublicKey(enrolled.pem(t))
	r, code := newReq(t)
	err := Check(pub, r, code, attacker.answer(t, r, code), t0.Add(time.Second))
	if !errors.Is(err, ErrBadSig) {
		t.Fatalf("want ErrBadSig, got %v", err)
	}
}

// A real signature from an earlier request must not satisfy a new one, even when the
// attacker guesses the same code: id and nonce are bound into what was signed.
func TestReplayedSignatureRejected(t *testing.T) {
	p := newPhone(t)
	pub, _ := ParsePublicKey(p.pem(t))
	old, oldCode := newReq(t)
	oldResp := p.answer(t, old, oldCode)

	fresh, _ := newReq(t)
	// Same id claimed, so only the signature binding can catch it.
	replay := Response{ID: fresh.ID, Chosen: oldCode, Sig: oldResp.Sig}
	if err := Check(pub, fresh, oldCode, replay, t0.Add(time.Second)); !errors.Is(err, ErrBadSig) {
		t.Fatalf("replay with matching code: want ErrBadSig, got %v", err)
	}
	// And the old response against the new request's id is a plain mismatch.
	if err := Check(pub, fresh, oldCode, oldResp, t0.Add(time.Second)); !errors.Is(err, ErrWrongID) {
		t.Fatalf("old response: want ErrWrongID, got %v", err)
	}
}

// The signature binds user, host and service, so an approval for one cannot be reused for
// another even by an attacker who can edit the request in flight.
func TestSignatureBindsContext(t *testing.T) {
	p := newPhone(t)
	pub, _ := ParsePublicKey(p.pem(t))
	r, code := newReq(t)
	resp := p.answer(t, r, code)
	for name, mut := range map[string]func(*Request){
		"user":    func(q *Request) { q.User = "root" },
		"host":    func(q *Request) { q.Host = "other" },
		"service": func(q *Request) { q.Service = "login" },
		"expires": func(q *Request) { q.Expires += 3600 },
		"nonce":   func(q *Request) { q.Nonce = strings.Repeat("0", 32) },
	} {
		q := r
		mut(&q)
		if err := Check(pub, q, code, resp, t0.Add(time.Second)); !errors.Is(err, ErrBadSig) {
			t.Errorf("changing %s: want ErrBadSig, got %v", name, err)
		}
	}
}

func TestGarbageSignature(t *testing.T) {
	p := newPhone(t)
	pub, _ := ParsePublicKey(p.pem(t))
	r, code := newReq(t)
	for _, sig := range []string{"", "not base64!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if err := Check(pub, r, code, Response{ID: r.ID, Chosen: code, Sig: sig}, t0.Add(time.Second)); !errors.Is(err, ErrBadSig) {
			t.Errorf("sig %q: want ErrBadSig, got %v", sig, err)
		}
	}
}

func TestParsePublicKeyRejectsOtherCurves(t *testing.T) {
	k, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if _, err := PublicKeyPEM(der); err == nil {
		t.Fatal("accepted a P-384 key")
	}
	if _, err := ParsePublicKey([]byte("hello")); err == nil {
		t.Fatal("accepted non-PEM")
	}
}
