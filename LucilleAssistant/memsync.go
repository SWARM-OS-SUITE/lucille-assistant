package main

// Shared-memory sync for paired phones.
//
// A phone's `swarm-memory sync` talks the LucyServer sync protocol (GET/POST
// /api/memory/sync) and authenticates with X-Swarm-Auth. Instead of handing every
// phone the swarm-wide SWARM_AUTH_TOKEN, the phone presents the device token it got
// from the one-time pairing code, and this service (which already knows paired
// devices) relays the call to LucyServer on loopback, where LucyServer needs no token.
// Only that one path is relayed, only for a paired person's device (or the main token /
// someone at this PC) — never for an unpaired caller or Dr. Boltz's agent.

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

const memorySyncPath = "/api/memory/sync"

// memorySyncUpstream is LucyServer's loopback address; override with LUCILLE_MEMORY_SYNC_UPSTREAM.
func memorySyncUpstream() string {
	return envOr("LUCILLE_MEMORY_SYNC_UPSTREAM", "http://127.0.0.1:5000")
}

func (a *App) handleMemorySync(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireHuman(w, r); !ok {
		return
	}
	target, err := url.Parse(memorySyncUpstream())
	if err != nil || target.Host == "" {
		writeErr(w, http.StatusInternalServerError, "Memory sync upstream is misconfigured.")
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = memorySyncPath
			pr.Out.Host = target.Host
			// The phone's device token is for this service only; LucyServer trusts loopback.
			for _, h := range []string{"X-Swarm-Auth", "Authorization", "Origin", "Referer", "Cookie", "X-Forwarded-For"} {
				pr.Out.Header.Del(h)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeErr(w, http.StatusBadGateway, "LucyServer's memory sync isn't reachable on this PC.")
		},
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	proxy.ServeHTTP(w, r)
}
