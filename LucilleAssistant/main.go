// Lucille Assistant — personal daily-life agent for SWARM OS.
// Port 8403. Serves the phone web app at / and a JSON API at /api/*.
// Loopback callers need no token. Anyone else (Tailscale phones) must send
// X-Swarm-Auth or ?token= matching either SWARM_AUTH_TOKEN or a per-device
// token from `lucille-assistant pair` (see pairing.go) — except /api/pair
// itself, which a device with no token yet uses to get one.
package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const version = "1.14.0"

//go:embed web
var webFS embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "nodes" { // run on this PC: pair or list other machines
		os.Exit(runNodesCLI(os.Args[2:], os.Stdout))
	}
	if len(os.Args) > 1 && os.Args[1] == "actions" { // run on this PC: sudoers-template | list
		var sb strings.Builder
		rc := runActionsCLI(os.Args[2:], &sb)
		fmt.Print(sb.String())
		os.Exit(rc)
	}
	if len(os.Args) > 1 && os.Args[1] == "pair" { // run on this PC: print a one-time code for a phone
		var sb strings.Builder
		rc := runPairCLI(os.Args[2:], &sb)
		fmt.Print(sb.String())
		os.Exit(rc)
	}
	if len(os.Args) > 1 && os.Args[1] == "devices" { // run on this PC: list | revoke a paired phone
		var sb strings.Builder
		rc := runDevicesCLI(os.Args[2:], &sb)
		fmt.Print(sb.String())
		os.Exit(rc)
	}
	if len(os.Args) > 1 && os.Args[1] == "bridge" { // run on this PC: the console for the Lucille <-> Dr. Boltz bridge
		log.SetOutput(io.Discard) // config-loading chatter would land in the tmux mirror
		os.Exit(runBridgeCLI(os.Args[2:], os.Stdout))
	}
	cfg := loadConfig()
	st, err := openStore(cfg.DataDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	mirrorThreadsToMemory = os.Getenv("LUCILLE_NO_MIRROR") == ""
	app := &App{cfg: cfg, store: st, cache: newCache(), hub: newHub(nodesPath()), pairing: newPairManager(st), keys: newKeyStore(keysFilePath())}
	if app.bridge, err = newBridge(cfg.DataDir, cfg.Bridge); err != nil {
		log.Fatalf("bridge: %v", err)
	}
	go app.hub.Run(context.Background())
	if n, err := st.SeedBriefings(time.Now()); err != nil {
		log.Printf("briefings: seeding failed: %v", err)
	} else if n > 0 {
		log.Printf("briefings: added %d built-in project briefings", n)
	}

	mux := app.newMux()

	handler := logRequests(auth(cfg.Token, st, mux))
	addrs := strings.Split(cfg.Listen, ";")
	for i, a := range addrs {
		a = strings.TrimSpace(a)
		srv := &http.Server{Addr: a, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
		if i == len(addrs)-1 {
			log.Printf("lucille-assistant %s listening on %s", version, a)
			log.Fatal(srv.ListenAndServe())
		}
		go func() {
			// Extra addresses (e.g. the Tailscale IP) may not exist yet at boot — retry.
			for {
				log.Printf("lucille-assistant listening on %s", a)
				err := srv.ListenAndServe()
				log.Printf("listen %s: %v (retry in 10s)", a, err)
				time.Sleep(10 * time.Second)
			}
		}()
	}
}

// newMux is the whole route table. It is a method (not inline in main) so tests can
// exercise the real routes behind the real auth().
func (app *App) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true, "service": "lucille-assistant", "version": version})
	})
	mux.HandleFunc("GET /api/brief", app.handleBrief)
	mux.HandleFunc("GET /api/projectbrief", app.handleProjectBrief)
	mux.HandleFunc("POST /api/projectbrief/{id}/think", browserSafe(app.handleProjectThink))
	mux.HandleFunc("POST /api/projectbrief/{id}/md", browserSafe(app.handleProjectMD))
	mux.HandleFunc("POST /api/projectbrief/{id}/boltz", browserSafe(app.handleProjectBoltz))
	mux.HandleFunc("GET /api/git", app.handleGit)
	mux.HandleFunc("GET /api/drives", app.handleDrives)
	mux.HandleFunc("GET /api/gdrive", app.handleGDrive)
	mux.HandleFunc("GET /api/mail", app.handleMail)
	mux.HandleFunc("GET /api/calendar", app.handleCalendar)
	mux.HandleFunc("GET /api/memory", app.handleMemory)
	mux.HandleFunc("GET "+memorySyncPath, browserSafe(app.handleMemorySync))
	mux.HandleFunc("POST "+memorySyncPath, browserSafe(app.handleMemorySync))
	mux.HandleFunc("POST /api/memory", app.handleMemoryAdd)
	mux.HandleFunc("GET /api/telemetry", app.handleTelemetry)
	mux.HandleFunc("GET /api/telemetry/stream", app.handleTelemetryStream)
	mux.HandleFunc("GET /api/rig", app.handleRig)
	mux.HandleFunc("POST /api/rig/advice", browserSafe(app.handleRigAdvice))
	mux.HandleFunc("GET /api/actions", browserSafe(app.handleActions))
	mux.HandleFunc("POST /api/actions/{id}/run", browserSafe(app.handleActionRun))
	mux.HandleFunc("GET /api/pair/new", browserSafe(app.handlePairNew))
	mux.HandleFunc("GET /api/bridge", browserSafe(app.handleBridgeState))
	mux.HandleFunc("GET /api/bridge/stream", browserSafe(app.handleBridgeStream))
	mux.HandleFunc("POST /api/bridge/say", browserSafe(app.handleBridgeSay))
	mux.HandleFunc("POST /api/bridge/challenges", browserSafe(app.handleBridgeChallengeCreate))
	mux.HandleFunc("POST /api/bridge/generate", browserSafe(app.handleBridgeGenerate))
	mux.HandleFunc("POST /api/bridge/challenges/{id}/decision", browserSafe(app.handleBridgeDecision))
	mux.HandleFunc("GET /api/bridge/inbox", browserSafe(app.handleBridgeInbox))
	mux.HandleFunc("POST /api/bridge/challenges/{id}/claim", browserSafe(app.handleBridgeClaim))
	mux.HandleFunc("POST /api/bridge/challenges/{id}/log", browserSafe(app.handleBridgeLog))
	mux.HandleFunc("POST /api/bridge/challenges/{id}/complete", browserSafe(app.handleBridgeComplete))
	mux.HandleFunc("POST /api/bridge/actions/{id}/request", browserSafe(app.handleBridgeActionRequest))
	mux.HandleFunc("GET /api/bridge/approvals", browserSafe(app.handleBridgeApprovals))
	mux.HandleFunc("GET /api/bridge/approvals/{id}", browserSafe(app.handleBridgeApprovalGet))
	mux.HandleFunc("POST /api/bridge/approvals/{id}/decision", browserSafe(app.handleBridgeApprovalDecision))
	mux.HandleFunc("POST /api/pair", app.handlePairRedeem)
	mux.HandleFunc("POST /api/unlock/request", app.handleUnlockRequest)
	mux.HandleFunc("GET /api/unlock/result/{id}", app.handleUnlockResult)
	mux.HandleFunc("GET /api/unlock/pending", app.handleUnlockPending)
	mux.HandleFunc("GET /api/unlock/stream", app.handleUnlockStream)
	mux.HandleFunc("POST /api/unlock/respond", app.handleUnlockRespond)
	mux.HandleFunc("GET /api/unlock/enrol/new", app.handleUnlockEnrolNew)
	mux.HandleFunc("POST /api/unlock/enrol", app.handleUnlockEnrol)
	mux.HandleFunc("GET /api/briefings", app.handleBriefings)
	mux.HandleFunc("POST /api/briefings", app.handleBriefingCreate)
	mux.HandleFunc("GET /api/briefings/{id}", app.handleBriefingGet)
	mux.HandleFunc("PUT /api/briefings/{id}", app.handleBriefingUpdate)
	mux.HandleFunc("DELETE /api/briefings/{id}", app.handleBriefingDelete)
	mux.HandleFunc("GET /api/care", app.handleCare)
	mux.HandleFunc("POST /api/care/med", app.handleCareMed)
	mux.HandleFunc("POST /api/care/water", app.handleCareWater)
	mux.HandleFunc("POST /api/care/meal", app.handleCareMeal)
	mux.HandleFunc("POST /api/care/sleep", app.handleCareSleep)
	mux.HandleFunc("GET /api/nudges", app.handleNudges)
	mux.HandleFunc("GET /api/sessions", app.handleSessions)
	mux.HandleFunc("POST /api/sessions/start", app.handleSessionStart)
	mux.HandleFunc("POST /api/sessions/stop", app.handleSessionStop)
	mux.HandleFunc("GET /api/gaming", app.handleGaming)
	mux.HandleFunc("GET /api/news", app.handleNews)
	mux.HandleFunc("GET /api/deals", app.handleDeals)
	mux.HandleFunc("GET /api/deals/lookup", app.handleDealLookup)
	mux.HandleFunc("GET /api/ideas", app.handleIdeas)
	mux.HandleFunc("GET /api/dispatch", app.handleDispatch)
	mux.HandleFunc("GET /api/swarm/status", app.handleSwarmStatus)
	mux.HandleFunc("GET /api/swarm/next", app.handleSwarmNext)
	mux.HandleFunc("POST /api/swarm/next", app.handleSwarmNext)
	mux.HandleFunc("GET /api/ai", browserSafe(app.handleAI))
	mux.HandleFunc("POST /api/ai/key", browserSafe(app.handleKeySet))
	mux.HandleFunc("POST /api/ai/key/test", browserSafe(app.handleKeyTest))
	mux.HandleFunc("POST /api/ai/prefs", browserSafe(app.handleAIPrefs))
	mux.HandleFunc("GET /api/ai/voices", browserSafe(app.handleAIVoices))
	mux.HandleFunc("POST /api/ai/voice", browserSafe(app.handleAIVoiceSet))
	mux.HandleFunc("POST /api/chat", app.handleChat)
	mux.HandleFunc("POST /api/chat/i30ltz", app.handleChatI30ltz) // Dr. I30LTZ: own persona, local models only (persona.go)
	mux.HandleFunc("GET /api/comments", app.handleComments)          // comments on suggestions + the schedule (comments.go)
	mux.HandleFunc("POST /api/comments/{id}", app.handleCommentAdd)
	mux.HandleFunc("GET /api/artifact", app.handleArtifact)                              // the team Brief + its threads (threads.go)
	mux.HandleFunc("GET /api/jobs", app.handleJobs)
	mux.HandleFunc("POST /api/jobs/draft", browserSafe(app.handleJobDraft))
	mux.HandleFunc("POST /api/jobs", browserSafe(app.handleJobCreate))
	mux.HandleFunc("POST /api/clipboard", browserSafe(app.handleClipboard))
	mux.HandleFunc("POST /api/jobs/{id}/claim", browserSafe(app.handleJobClaim))
	mux.HandleFunc("POST /api/jobs/{id}/done", browserSafe(app.handleJobDone))
	mux.HandleFunc("POST /api/jobs/{id}/cancel", browserSafe(app.handleJobCancel))
	mux.HandleFunc("GET /api/game/search", app.handleGameSearch)
	mux.HandleFunc("GET /api/game", app.handleGame)
	mux.HandleFunc("GET /api/games/marks", app.handleGameMarks)
	mux.HandleFunc("POST /api/games/mark", browserSafe(app.handleGameMark))
	mux.HandleFunc("GET /api/sight/ping", app.handleSightPing)
	mux.HandleFunc("POST /api/sight", app.handleSight)
	mux.HandleFunc("POST /api/remember", browserSafe(app.handleRemember))
	mux.HandleFunc("POST /api/threads/post", browserSafe(app.handleThreadPost))
	mux.HandleFunc("POST /api/threads/ask", browserSafe(app.handleThreadAsk))
	mux.HandleFunc("POST /api/threads/deal", browserSafe(app.handleThreadDeal))
	mux.HandleFunc("GET /api/threads/stream", app.handleThreadStream)
	mux.HandleFunc("POST /api/tts", app.handleTTS)

	sub, _ := fs.Sub(webFS, "web")
	page, _ := fs.ReadFile(sub, "index.html")
	// index.html is written as page content (same file publishes as the
	// artifact), so the server adds the document shell around it.
	shell := append([]byte("<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">\n"), page...)
	files := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(shell)
			return
		}
		files.ServeHTTP(w, r)
	})
	return mux
}

// presentedToken is the credential a request carries, from a header or ?token= (an
// EventSource cannot set headers, so streams use the query form).
func presentedToken(r *http.Request) string {
	got := r.Header.Get("X-Swarm-Auth")
	if got == "" {
		got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if got == "" {
		got = r.URL.Query().Get("token")
	}
	return got
}

// auth decides who is calling and passes that on (see identity.go). A token that names
// a paired device identifies that device even from loopback, so Dr. Boltz's agent stays
// "the agent" when it talks to this service from the same PC.
func auth(token string, store *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Swarm-Auth")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		serve := func(c Caller) { next.ServeHTTP(w, withCaller(r, c)) }

		got := presentedToken(r)
		if got != "" {
			if d, ok := store.DeviceForToken(got); ok {
				serve(callerForDevice(d))
				return
			}
			if token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
				serve(Caller{Kind: callerMain})
				return
			}
		}
		bad := got != "" // a token was sent and matched nothing

		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			serve(Caller{Kind: callerPC, BadToken: bad})
			return
		}
		// Redeeming a pairing code is how a device with no token yet gets one; it must
		// be reachable with none. The code itself (checked inside the handler, rate
		// limited and single-use) is what gates this, not a token.
		if r.URL.Path == "/api/pair" {
			serve(Caller{Kind: callerOpen})
			return
		}
		// Static files (the app shell) are public so the phone can load the
		// page and then ask for the token; data needs the token.
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			serve(Caller{Kind: callerOpen})
			return
		}
		if token == "" {
			serve(Caller{Kind: callerOpen, BadToken: bad})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		writeJSON(w, map[string]any{"ok": false, "error": "Wrong or missing token."})
	})
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(t).Round(time.Millisecond))
		}
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	writeJSON(w, map[string]any{"ok": false, "error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
