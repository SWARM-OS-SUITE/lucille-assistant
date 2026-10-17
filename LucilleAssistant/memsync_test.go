package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMemorySyncRelaysForAPairedPhoneWithoutForwardingItsToken(t *testing.T) {
	var gotAuth, gotQuery, gotBody, gotMethod string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Swarm-Auth") + r.Header.Get("Authorization")
		gotQuery, gotMethod = r.URL.RawQuery, r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Write([]byte(`{"entries":[],"cursor":"7","more":false}`))
	}))
	defer up.Close()
	t.Setenv("LUCILLE_MEMORY_SYNC_UPSTREAM", up.URL)

	_, h, tok := setupUnlock(t)

	w := phoneReq(h, "GET", "/api/memory/sync?cursor=3", tok, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cursor":"7"`) {
		t.Fatalf("paired phone GET: %d %s", w.Code, w.Body.String())
	}
	if gotQuery != "cursor=3" || gotMethod != "GET" {
		t.Fatalf("upstream saw %s ?%s", gotMethod, gotQuery)
	}
	if gotAuth != "" {
		t.Fatalf("the phone's device token must not reach LucyServer, got %q", gotAuth)
	}

	w = phoneReq(h, "POST", "/api/memory/sync", tok, `{"entries":[]}`)
	if w.Code != 200 || gotMethod != "POST" || gotBody != `{"entries":[]}` {
		t.Fatalf("paired phone POST: %d method=%s body=%q", w.Code, gotMethod, gotBody)
	}
}

func TestMemorySyncRefusesUnpairedAndBadTokens(t *testing.T) {
	called := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer up.Close()
	t.Setenv("LUCILLE_MEMORY_SYNC_UPSTREAM", up.URL)

	_, h, _ := setupUnlock(t)
	for name, tok := range map[string]string{"no token": "", "wrong token": "not-a-real-token"} {
		w := phoneReq(h, "GET", "/api/memory/sync", tok, "")
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: want 401, got %d", name, w.Code)
		}
	}
	if called {
		t.Fatal("an unauthenticated caller reached LucyServer")
	}
}

func TestMemorySyncSaysSoWhenLucyServerIsDown(t *testing.T) {
	t.Setenv("LUCILLE_MEMORY_SYNC_UPSTREAM", "http://127.0.0.1:1")
	_, h, tok := setupUnlock(t)
	if w := phoneReq(h, "GET", "/api/memory/sync", tok, ""); w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", w.Code)
	}
}
