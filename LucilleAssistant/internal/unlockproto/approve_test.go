package unlockproto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeService is the untrusted carrier. onRequest lets a test play the phone (or an
// attacker) as soon as the helper posts a request.
type fakeService struct {
	mu        sync.Mutex
	req       *Request
	answer    *resultReply
	onRequest func(f *fakeService, r Request)
	down      bool
	hits      int
}

func (f *fakeService) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/unlock/request", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hits++
		if f.down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var q Request
		if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		f.req = &q
		if f.onRequest != nil {
			f.onRequest(f, q)
		}
	})
	mux.HandleFunc("GET /api/unlock/result/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := resultReply{State: "pending"}
		if f.answer != nil {
			out = *f.answer
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	return mux
}

func (f *fakeService) set(rr resultReply) { f.answer = &rr }

func (f *fakeService) clear() { f.mu.Lock(); f.answer = nil; f.mu.Unlock() }

func (f *fakeService) snapshot() (answer *resultReply, hits int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.answer, f.hits
}

// syncBuf is a bytes.Buffer safe for the helper to write while the fake phone reads.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
func (s *syncBuf) Reset()                      { s.mu.Lock(); defer s.mu.Unlock(); s.b.Reset() }

type rig struct {
	t     *testing.T
	svc   *fakeService
	srv   *httptest.Server
	phone phone
	cfg   Config
	out   *syncBuf
	clock *time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	svc := &fakeService{}
	srv := httptest.NewServer(svc.handler())
	t.Cleanup(srv.Close)
	p := newPhone(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "approver.pub")
	if err := os.WriteFile(keyPath, p.pem(t), 0o644); err != nil {
		t.Fatal(err)
	}
	now := t0
	np := &now
	out := &syncBuf{}
	return &rig{t: t, svc: svc, srv: srv, phone: p, out: out, clock: np, cfg: Config{
		API: srv.URL, PubKeyPath: keyPath, StateDir: filepath.Join(dir, "state"),
		Service: "sudo", User: "i30ltz", Host: "debian",
		TTL: 45 * time.Second, Poll: 2 * time.Millisecond, Out: out,
		Now: func() time.Time { return *np },
	}}
}

// codeShown pulls the 4 digits out of what the user would see on screen.
func (r *rig) codeShown() string {
	for _, f := range strings.Fields(r.out.String()) {
		if len(f) == 4 && strings.Trim(f, "0123456789") == "" {
			return f
		}
	}
	r.t.Fatalf("no code in output %q", r.out.String())
	return ""
}

func (r *rig) run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return Approve(ctx, r.cfg)
}

// phoneAnswers makes the fake service answer as soon as it sees the request. pick chooses
// which number the phone taps; it is handed the real code only to build wrong-answer cases.
func (r *rig) phoneAnswers(pick func(req Request, code string) string) {
	r.svc.onRequest = func(f *fakeService, q Request) {
		// The code is only on the PC screen; read it back from the helper's output once it
		// is printed. The helper prints after the POST returns, so answer asynchronously.
		go func() {
			var code string
			for i := 0; i < 500 && code == ""; i++ {
				time.Sleep(time.Millisecond)
				for _, w := range strings.Fields(r.out.String()) {
					if len(w) == 4 && strings.Trim(w, "0123456789") == "" {
						code = w
					}
				}
			}
			chosen := pick(q, code)
			resp := r.phone.answer(r.t, q, chosen)
			f.mu.Lock()
			f.set(resultReply{State: "answered", Chosen: resp.Chosen, Sig: resp.Sig})
			f.mu.Unlock()
		}()
	}
}

func TestApproveSuccess(t *testing.T) {
	r := newRig(t)
	r.phoneAnswers(func(_ Request, code string) string { return code })
	if err := r.run(); err != nil {
		t.Fatalf("want approval, got %v", err)
	}
}

func TestApproveNoAnswerFallsThrough(t *testing.T) {
	r := newRig(t)
	// The clock jumps 10s every time it is read, so the 45s window closes quickly.
	n := 0
	r.cfg.Now = func() time.Time { n++; return t0.Add(time.Duration(n) * 10 * time.Second) }
	err := r.run()
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

func TestApproveServiceDown(t *testing.T) {
	r := newRig(t)
	r.srv.Close()
	if err := r.run(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
	r2 := newRig(t)
	r2.svc.mu.Lock()
	r2.svc.down = true
	r2.svc.mu.Unlock()
	if err := r2.run(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("503: want ErrUnavailable, got %v", err)
	}
}

func TestApproveNoEnrolledKey(t *testing.T) {
	r := newRig(t)
	r.cfg.PubKeyPath = filepath.Join(t.TempDir(), "missing.pub")
	if err := r.run(); err == nil {
		t.Fatal("approved with no key on disk")
	}
	if _, h := r.svc.snapshot(); h != 0 {
		t.Fatal("contacted the service without a key to verify against")
	}
}

func TestApproveWrongNumber(t *testing.T) {
	r := newRig(t)
	r.phoneAnswers(func(q Request, code string) string {
		for _, c := range q.Choices {
			if c != code {
				return c
			}
		}
		return ""
	})
	if err := r.run(); !errors.Is(err, ErrWrongNumber) {
		t.Fatalf("want ErrWrongNumber, got %v", err)
	}
}

// A hijacked service that answers "approved" with its own key must not get in.
func TestApproveForgedByService(t *testing.T) {
	r := newRig(t)
	attacker := newPhone(t)
	r.svc.onRequest = func(f *fakeService, q Request) {
		go func() {
			// Even if the attacker somehow knew the code, the signature is not the enrolled key's.
			time.Sleep(5 * time.Millisecond)
			resp := attacker.answer(t, q, q.Choices[0])
			f.mu.Lock()
			f.set(resultReply{State: "answered", Chosen: resp.Chosen, Sig: resp.Sig})
			f.mu.Unlock()
		}()
	}
	err := r.run()
	if err == nil {
		t.Fatal("approved a forged answer")
	}
}

func TestApproveLockoutAfterThreeFailures(t *testing.T) {
	r := newRig(t)
	r.phoneAnswers(func(q Request, code string) string {
		for _, c := range q.Choices {
			if c != code {
				return c
			}
		}
		return ""
	})
	for i := 0; i < 3; i++ {
		r.svc.clear()
		r.out.Reset()
		if err := r.run(); !errors.Is(err, ErrWrongNumber) {
			t.Fatalf("round %d: want ErrWrongNumber, got %v", i, err)
		}
	}
	// Now even a perfect phone is refused, without contacting the service at all.
	r.phoneAnswers(func(_ Request, code string) string { return code })
	r.svc.clear()
	_, hits := r.svc.snapshot()
	if err := r.run(); !errors.Is(err, ErrLockedOut) {
		t.Fatalf("want ErrLockedOut, got %v", err)
	}
	if _, h := r.svc.snapshot(); h != hits {
		t.Fatal("locked-out helper still contacted the service")
	}
	// After the lockout window it works again.
	later := t0.Add(lockDuration + time.Second)
	*r.clock = later
	r.out.Reset()
	if err := r.run(); err != nil {
		t.Fatalf("after lockout: want approval, got %v", err)
	}
}

func TestApproveTimeoutsDoNotLockOut(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 5; i++ {
		n := 0
		r.cfg.Now = func() time.Time { n++; return t0.Add(time.Duration(n) * 10 * time.Second) }
		if err := r.run(); !errors.Is(err, ErrExpired) {
			t.Fatalf("round %d: want ErrExpired, got %v", i, err)
		}
	}
	r.cfg.Now = func() time.Time { return t0 }
	r.phoneAnswers(func(_ Request, code string) string { return code })
	r.svc.clear()
	r.out.Reset()
	if err := r.run(); err != nil {
		t.Fatalf("ignored prompts locked the user out: %v", err)
	}
}

// Only the first answer counts: once the helper has decided it stops listening, so a
// second (late or replayed) answer can change nothing.
func TestApproveOneShot(t *testing.T) {
	r := newRig(t)
	r.phoneAnswers(func(_ Request, code string) string { return code })
	if err := r.run(); err != nil {
		t.Fatal(err)
	}
	// A new run issues a fresh request with a fresh id and nonce; last time's answer, still
	// sitting in the carrier, is useless.
	ans, _ := r.svc.snapshot()
	stale := *ans
	r.svc.mu.Lock()
	r.svc.onRequest = nil
	r.svc.answer = &stale
	r.svc.mu.Unlock()
	r.out.Reset()
	if err := r.run(); err == nil {
		t.Fatal("a stale answer from the previous request was accepted")
	}
}
