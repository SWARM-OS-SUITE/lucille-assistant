# Lucille Assistant v1.7.0

Lucille's personal-assistant side: a Go service on **:8403** plus a phone-first web app
(the purple-lightning UI in `web/`). Stdlib only — no dependencies.

## What she does (built)

| Job | Endpoint | Source |
|---|---|---|
| Daily brief + spoken headline | `GET /api/brief` | everything below, in parallel |
| **Project brief**: reads the real project files, suggestions, add to the project md, send to Dr. Boltz | `GET /api/projectbrief`, `POST /api/projectbrief/{id}/{think,md,boltz}` | see **Project brief** below |
| Nudges: meds late, water behind pace, meal gap, short sleep, 90-min break, past 1 AM | `GET /api/nudges` | care log + sessions |
| Meds / water / meals / sleep | `GET /api/care`, `POST /api/care/{med,water,meal,sleep}` | `~/.local/share/lucille-assistant/care/<date>.json` |
| Project + gaming time | `GET /api/sessions`, `POST /api/sessions/{start,stop}` | `sessions.json` |
| Git status | `GET /api/git` | `git status` on configured repos |
| Local drives | `GET /api/drives` | `/proc/mounts` + statfs |
| Mail / calendar / Google Drive | `GET /api/{mail,calendar,gdrive}` | GoogleWorkspaceService :8399 |
| Shared memory read / add | `GET/POST /api/memory` | `/mnt/brains/swarm-memory/log`, `swarm-memory add` |
| Telemetry for this PC and every paired node | `GET /api/telemetry`, `GET /api/telemetry/stream` (SSE) | `/proc` + `/sys` here; each node's `i30ltz-node` agent |
| Project briefings (editable, 4 built in) | `GET/POST /api/briefings`, `GET/PUT/DELETE /api/briefings/{id}` | `~/.local/share/lucille-assistant/briefings/<id>.json` |
| PC-control allowlist (reboot, restart a service, drop caches, …) | `GET /api/actions`, `POST /api/actions/{id}/run` | fixed list in `config.json`, see **Actions** below |
| Device pairing (a phone gets its own token) | `GET /api/pair/new` (loopback only), `POST /api/pair` | `~/.local/share/lucille-assistant/paired.json`, see **Pairing** below |
| Dr. Boltz bridge: Lucille's challenges, the shared transcript, approvals | `/api/bridge/*` | see **Dr. Boltz bridge** below |
| Steam hours | `GET /api/gaming` | Steam Web API + logged gaming sessions |
| Steam sales + CD-key prices | `GET /api/deals`, `/api/deals/lookup?title=` | CheapShark (authorized key stores only) |
| Arch / distro / Linux / Linux-gaming news, politics filtered | `GET /api/news?tag=` | RSS: Arch, CachyOS, GamingOnLinux, Phoronix, DistroWatch, Steam |
| Build ideas | `GET /api/ideas` | Nemotron with git + memory + hours context; curated fallback |

## Telemetry and nodes (1.2)

`GET /api/telemetry` is a snapshot and `GET /api/telemetry/stream` is **one SSE stream** (`event: nodes`, a full snapshot every 2 s, a `: ping`
comment every 15 s) that carries **this PC and every paired node**. The fields match the I30LTZ node agent's `/v1/telemetry`
(`cpu_percent`, `ram_*`, `storage_*`, `load1`, `uptime_seconds`, `temp_c`) plus `cores`, `swap_percent` and `net_rx_kbps` / `net_tx_kbps`.
**A value that could not be measured is `-1` (or absent), never a made-up zero**, and a node that does not send a field shows it as unknown.

Other machines join by running the node agent there (`i30ltz-node serve`, then `i30ltz-node pair` for a one-time code) and pairing **on this PC**:

```bash
lucille-assistant nodes add NAME http://<node-tailscale-ip>:7437 CODE   # pairs; stores the token in nodes.json (mode 0600)
lucille-assistant nodes list                                            # never prints tokens
lucille-assistant nodes remove NAME                                     # then also: i30ltz-node revoke ... on that machine
```

`nodes.json` sits next to `config.json` (`LUCILLE_ASSISTANT_NODES` overrides). The running service reloads it within 10 s, follows each node's
`/v1/events` stream, and shows a node as offline (with its last known numbers and a short reason) when it stops sending. **Node tokens stay
on this PC: the phone only ever talks to this service**, and no endpoint returns or accepts a node token.

## Actions (1.3)

A fixed, PC-defined command allowlist — never an arbitrary string from a phone. Each entry in `config.json`'s `actions` array is a
literal argv (`check-mounts`, `restart-lucyserver`, `drop-ram-caches`, `reboot-pc`, `shutdown-pc` ship as defaults); a request can only
pick one by `id` and flip `confirm`, and no field in the request body is ever substituted into a command. `GET /api/actions` lists them
(with the exact command shown, for transparency); `POST /api/actions/{id}/run` with `{"confirm": true}` runs one — destructive actions
refuse without `confirm`, runs are serialized (one at a time, PC-wide) and logged to `~/.local/share/lucille-assistant/actions.log`.

A `sudo: true` action runs under `sudo -n`, which fails instantly ("a password is required") instead of hanging if the matching
NOPASSWD rule isn't installed. Nothing runs as root until you do that yourself:

```bash
lucille-assistant actions sudoers-template   # prints the NOPASSWD lines to review
sudo visudo -c -f /tmp/lucille-assistant.sudoers && sudo install -o root -g root -m 0440 ... /etc/sudoers.d/lucille-assistant
```

## Pairing (1.4)

A phone gets its own permanent token instead of retyping `SWARM_AUTH_TOKEN` (48 hex characters, no clipboard paste on most keyboards,
and painful to retype every time it rotates). Run this on the PC:

```bash
lucille-assistant pair            # prints a 6-digit code: one device, one use, 10 minutes
lucille-assistant pair --role agent   # same, but the device that redeems it becomes Dr. Boltz's worker (1.5)
lucille-assistant devices list    # never prints a raw token, only id/label/timestamps
lucille-assistant devices revoke "label or id"
```

Enter the code in the app (Settings -> Pair with this PC). `GET /api/pair/new` (the code generator) only ever answers a loopback
caller — even a remote request holding the valid `SWARM_AUTH_TOKEN` is refused, because minting new device access has to stay
physically-at-the-PC. `POST /api/pair` (redeeming a code) is reachable from anywhere, Tailscale included: the code itself, not the
network path, is the proof. A paired device's token is independent of `SWARM_AUTH_TOKEN` — rotating the PC's main token never breaks
an already-paired phone — and is stored only as a SHA-256 hash in `paired.json`. Five wrong codes lock pairing for 60 s; a code is
single-use the instant it matches, even within its 10-minute window.

## Dr. Boltz bridge (1.5)

Lucille thinks up **challenges**; Dr. Boltz codes them on disk and commits them; **you** decide what lands.
Phones, the PC terminal, Lucille and Dr. Boltz all read one shared transcript (the "mirrored terminal").

```
phone (paired) ──┐                      ┌─ Lucille :8403 ── thinks up challenges, answers in the channel
PC: bridge term ─┼──►  /api/bridge/*  ◄─┤
                 │                      └─ Dr. Boltz worker (paired as role "agent")
                 │                           git worktree per challenge → sandboxed engine edits files
                 │                           → scope check + secret scan + tests (sandboxed) → commit on boltz/cNNNN
you approve ─────┘                           → after your approval: fast-forward main. Nothing is ever pushed.
```

### Set it up (once)

1. **Install 1.5**: `/mnt/lucillebuild/scripts/build.sh go package`, then `scripts/cutover_live_service.sh` (backs up the unit, restarts, and
   rolls itself back if the new build is not healthy). Then check `bridge.repos` below: the default paths use `/mnt/lucillebuild`; if a
   drive is mounted somewhere else on this PC, set its real path in `~/.config/lucille-assistant/config.json`.
2. **Pair the worker** (the code is minted on this PC, like a phone's):
   `lucille-assistant pair --role agent`, then `python3 /mnt/recoverybuild/30-source/mad-dr-boltz/brain/boltz_bridge.py pair CODE`.
3. **Pair your phone** as before; the **Bridge** tab shows the queue, the channel, and what is waiting for you.
4. **Start Dr. Boltz** (it never starts by itself): `python3 .../boltz_bridge.py run`. It needs `git`, `bwrap`, `socat` and the `claude`
   command logged in (`claude auth status`); `boltz_bridge.py status` checks all of that. Any other engine: set `BOLTZ_ENGINE_CMD`.
5. **Watch it** with `lucille-assistant bridge term` (tmux: live transcript on top, a shell below; `--ro` to look without typing).

### Day to day

```bash
lucille-assistant bridge generate 3        # Lucille thinks up 3 challenges (a model if one answers, else a curated list)
lucille-assistant bridge challenge "Add X" --repo lucille --body "Done means: go test ./... passes and ..."
lucille-assistant bridge status            # Dr. Boltz online?, the queue, anything waiting for you
lucille-assistant bridge approve c0007     # lets Dr. Boltz fast-forward boltz/c0007 into main (or `reject`, `cancel`, `retry`)
lucille-assistant bridge allow|deny ID     # Dr. Boltz asked to run something as root: your answer
lucille-assistant bridge say "..." --to boltz
```

### Who can do what

| | person (PC, paired phone, main token) | Dr. Boltz (paired, role `agent`) |
|---|---|---|
| queue / generate / cancel / retry challenges | yes | no |
| approve or reject finished work | yes | no |
| claim a challenge, log progress, report the result | no | yes |
| merge work a person approved | no | yes (fast-forward only) |
| run an action directly (`/api/actions/{id}/run`) | yes | **no**, it must ask via the bridge |
| mint a pairing code | yes, on the PC | **no** |
| read the transcript, talk in the channel | yes | yes |

### Root ("sudo")

Nobody gets a root shell. Root exists only as the fixed **allowlist** in `config.json` (**Actions** above), each entry a literal command run
with `sudo -n`. **Nothing runs as root until you install the matching sudoers lines yourself** (`lucille-assistant actions sudoers-template`;
each line says whether Dr. Boltz may use it unattended). Until then every sudo action fails at once with "a password is required".
For Dr. Boltz, per action: `"agent_auto": true` runs it without asking (the defaults `kernel-warnings` and `boot-errors` are read-only
diagnostics), otherwise it **waits for your tap** on the phone or `bridge allow`. A destructive action (reboot, shut down) can never be requested
by the agent, and `agent_auto` is ignored on one. The coding engine itself cannot reach this service, and cannot use `sudo`: it asks by writing a
request file, and the worker relays it. Every run is in `actions.log`, with who asked and who approved.

### What was verified (and the limits)

Verified on this PC with the real service, the real worker and a real model (headless `claude`): a challenge went from queued to merged;
an agent token is refused when it tries to approve, queue, run an action directly, request a destructive one or mint a pairing code; the sandboxed
engine could not read the home directory, reach the internet, reach this service on loopback, or use `sudo`; a secret in a change, an edit outside
the repo's `scope`, binary files, symlinks out of the worktree and `.git*` edits are all refused before anything is committed; ordinary web pages
cannot drive these routes (see below). Limits you should know about:

- **Loopback is still trusted.** A request from this PC with no token has always meant "the person at the PC", so any program running as *you*
  can act as you, including approving. The coding engine is kept away from it by the sandbox, and the worker never does it; a hard boundary would
  mean running the worker as a separate user, which is not set up.
- **Cross-site guard covers only the powerful routes** (`/api/bridge/*`, `/api/actions*`, `/api/pair/new`): a web page in your browser can no
  longer queue code for Dr. Boltz, run an action or mint a pairing code. The older routes (mail, calendar, memory, care...) still answer any
  page that fetches `http://127.0.0.1:8403` because of the open `Access-Control-Allow-Origin: *`; closing that is a separate decision.
- **Cost.** One challenge is capped by `bridge.max_budget_usd` (default $2, split over at most three engine runs) and `max_minutes`. A trivial
  model turn already costs a few cents. The worker only works while you run it.
- **Auto-sync.** Merging into `/mnt/recoverybuild` main is picked up and pushed to GitHub by your existing sync job. The bridge itself never pushes.
- Dr. Boltz writes code that its checks then run (in a sandbox, no network). Read the diff before you approve.

### Config (`bridge` in `config.json`)

```json
"bridge": {
  "max_budget_usd": 2, "max_minutes": 20, "model": "",
  "repos": [
    {"name": "lucille", "path": "/mnt/lucillebuild/src/LucilleAssistant", "work_dir": "/mnt/lucillebuild/cache/boltz-work",
     "env_file": "/mnt/lucillebuild/env.sh", "verify": "test -z \"$(gofmt -l .)\" && go vet ./... && go test ./...", "desc": "..."},
    {"name": "boltz", "path": "/mnt/recoverybuild", "scope": "30-source/mad-dr-boltz", "work_dir": "/mnt/recoverybuild/.boltz-work",
     "verify": "python3 -I -m unittest discover -s brain -p 'test_*.py'", "desc": "..."}
  ]
}
```

`scope` limits where a change may land. `work_dir` holds the throw-away worktrees (same partition as the repo; if it sits inside the repo the
worker adds it to `.git/info/exclude` so `git add -A` jobs skip it). `verify` is yours, never taken from a challenge. Lucille hands this policy
to the worker on every poll, so a challenge cannot widen it.

### Bridge API

`GET /api/bridge` (state), `GET /api/bridge/stream?since=N` (SSE), `POST /api/bridge/say`, `POST /api/bridge/challenges`,
`POST /api/bridge/generate`, `POST /api/bridge/challenges/{id}/decision`, `GET|POST /api/bridge/approvals...` (people);
`GET /api/bridge/inbox`, `POST /api/bridge/challenges/{id}/claim|log|complete`, `POST /api/bridge/actions/{id}/request` (Dr. Boltz).
All are same-origin only and need a paired device or the main token.

## Project brief (1.6)

The phone's **Briefs -> Brief** tab. Lucille reads each project's files on this PC (the `read_first` list of its briefing), pulls
out what is open on the build list, and shows it as an artifact: a title bar, a document, code blocks with **Copy**.

- **Reads the PC, falls back to git.** `GET /api/projectbrief` opens only `.md`/`.txt` files inside `project_brief.roots` (symlinks are
  resolved first; hidden paths, names that look like secrets, and anything that is not a plain text file are refused and reported as
  `blocked`). Each file carries its GitHub address. If the PC does not answer, **the phone reads the same files from the public GitHub
  mirrors itself** (`raw.githubusercontent.com`, no token), labels the screen *from git* with the commit it read, and says which files
  exist only on the PC. A project with no readable build list shows "I could not read it", never an invented one. The last brief the
  PC gave is cached on the phone; if GitHub does not answer either, that copy is shown, labelled *cached*.
- **Suggestions**, in this order: ones Lucille thought up (tap *Ask Lucille for fresh ones*: one model call for one project, 20 s
  cooldown, nothing is spent unless you tap), else the numbered "3 SUGGESTIONS" in the briefing, else the top open build-list items.
  Each shows a ready-to-run command, `cd <project> && claude '<prompt>'`. **It is shown and copied, never run from the phone**: a
  network "run this string" endpoint is exactly what the PC-actions allowlist exists to avoid. The text is quoted so it is always one
  shell word.
- **Add to the project md**: `POST /api/projectbrief/{id}/md {title, why}` appends one dated line (`- 2026-10-12 lucille: LA suggestion
  from the Lucille brief (unvetted): ... [sg-xxxxxxxx]`) to the end of the *Idea inbox* section of `project_brief.md` (default
  `/mnt/recoverybuild/00-CURRENT/MASTER_BUILD_LIST.md`). The write is a temp file renamed over the original, dropped and retried if the
  file changed in between (other AIs and the auto-sync job edit it); the `[sg-...]` marker makes it idempotent, and an idea someone already wrote there by hand (an inbox line naming the project code and carrying the title's words) counts as added too, so the button reads "In project md" instead of adding it twice (strict on purpose: a miss costs one duplicate line, a false match would wrongly say "added"). The file must be
  inside a root and writable, and the caller must be a person (not Dr. Boltz).
- **Send to Dr. Boltz**: `POST /api/projectbrief/{id}/boltz {title, body, size}` queues a bridge challenge. The phone shows an editable
  dialog first, and the server never looks the text up itself, so text read from a file or written by a model cannot reach the coder
  without you reading it. Only a project with an entry in `project_repo` can be sent (default: `lucille-assistant` -> `lucille`);
  SWARM OS, Recovery OS and the Companion are **not** Dr. Boltz repos today, so their Send button is disabled with the reason. Adding one is
  a policy decision: put the repo under `bridge.repos` and map the project in `project_repo`.
- Adding and sending need the PC; both are disabled (with the reason) when the phone is showing git or cached data.
- Config (`project_brief` in `config.json`): `roots`, `md`, `md_section`, `aliases` (C is `/mnt/lucillebuild`, but udisks may mount it
  at `/mnt/lucilleassistant`; both spellings work and the commands use whichever exists), `git` (directory -> `owner/repo` + branch),
  `project_repo`, `dirs`. Files outside every git root (the two Lucille READMEs, the Boltz README) are PC-only by design.
- Tests: `projectbrief_test.go` (guards, tracker parser, md insertion and concurrency, shell quoting, the four endpoints and who may call
  them) and `ProjectBriefParseTest.kt` (the phone's copy of the tracker parser).

## Briefings

A briefing is a named, editable "read this first" document for a project: title, short project label, one-line
summary, a list of files to read first, and the body. They are the same context you give Claude and other AIs,
kept where Lucille can show it (**Briefings** tab, with *Copy for an AI*), edit it, and answer from it: her chat
prompt gets one line per briefing (title + summary, never the full text).

Each built-in is a **status briefing**: `STATUS` (dated, with a health colour), `NEXT STEPS & SCHEDULE` (every step owned by `[YOU]` or `[AI]`), `3 SUGGESTIONS`, then a short `REFERENCE`. Status and schedule are a dated snapshot written from the trackers, so they go stale: refresh them by editing the briefing (phone app or web) or by asking Claude to regenerate them, and keep the `seed/*.md` copies current for fresh installs (a test enforces the layout). Seeds apply only once per data folder, so to push new seed text to a running install, `PUT /api/briefings/<id>` it.

Four come built in, matching the four projects in `MASTER_BUILD_LIST.md`: **SWARM OS** (SW), **I30LTZ Recovery OS** (RC),
**I30LTZ Companion** (CP) and **Lucille Assistant** (LA). They live in `seed/*.md` and are compiled into the binary, so
the app stays stand-alone. On first start with a data directory they are copied into `<data>/briefings/`; after that
they are ordinary briefings you can edit or delete. A seed you deleted is never brought back, and one you edited is
never overwritten (the list of seeds already applied is `<data>/briefings/.seeded`). A new seed added in a later
version appears once. To change the defaults for fresh installs, edit `seed/*.md` and rebuild; existing installs keep
their own copies. Seed format: a `---` header (`title`, `project`, `order`, `summary`, `read_first` with paths
separated by ` | `), a second `---`, then the body. Seeds must not contain secrets (a test scans for common key shapes).

`POST /api/briefings` and `PUT /api/briefings/{id}` take `{"title","project","summary","body","read_first":[...]}`.
Limits: title 120, project label 40, summary 400 characters, body 100 KB, 20 paths, 200 briefings.

## Build & run

**This project lives and builds on its own partition, `/mnt/lucillebuild`** (since 2026-10-09; it used to be
`/mnt/Lucy/30-source/LucilleAssistant`). The toolchain (Go, JDK 21, Gradle, Android SDK) and caches are on that
partition too, so nothing else needs to be installed or mounted:

```bash
/mnt/lucillebuild/scripts/build.sh            # Go service + Android APK + dist/ (everything)
/mnt/lucillebuild/scripts/build.sh go         # service only (static binary)
/mnt/lucillebuild/scripts/build.sh apk        # com.lucille.app debug APK
# APP=lucille-android builds the older os.swarm.lucille app, which does not compile today (undefined color `Void`; see /mnt/lucillebuild/README.md)
```

Output is in `/mnt/lucillebuild/dist/` and runs **stand-alone** (no source tree, no build partition, no SWARM OS):
`lucille-assistant-<ver>-linux-amd64.tar.gz` (static binary + `install.sh` + user systemd unit + `env.example`) and
`LucilleAssistant-<ver>-debug.apk`. Install on a machine with `./install.sh`, then
`systemctl --user enable --now lucille-assistant`.

Open http://127.0.0.1:8403. First run writes `~/.config/lucille-assistant/config.json` —
**edit your real med names and times there**, plus repos, mounts, projects, water goal, wake/bed times.

Keys are read from, in order: the real environment, `$LUCILLE_ASSISTANT_ENV`, `.env` next to the binary,
`~/.config/lucille-assistant/.env`, then the legacy SWARM OS files (`/mnt/swarmbuild/20-runtime/.env`,
`/mnt/Lucy/20-runtime/.env`, `/etc/swarm_os.env`) if they happen to exist. None of them is required.
**Since 1.7 the paid cloud-AI keys (`OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`, `CLAUDE_API_KEY`) are never read from those legacy SWARM
files**: she spends only a key you gave her (the AI window, or her own `.env`).

Env: `LUCILLE_ASSISTANT_LISTEN` (`;`-separated), `LUCILLE_ASSISTANT_CONFIG`, `LUCILLE_ASSISTANT_DATA`,
`LUCILLE_ASSISTANT_ENV`, `SWARM_AUTH_TOKEN`, `STEAM_API_KEY`, `STEAM_ID64`, `NVIDIA_API_KEY`,
`OPENROUTER_API_KEY`, `OPENROUTER_MODEL`, `ANTHROPIC_API_KEY`, `ANTHROPIC_MODEL`, `ANTHROPIC_EFFORT`, `NVIDIA_MODEL`,
`ELEVEN_API_KEY`, `ELEVEN_VOICE_ID`, `LUCILLE_ASSISTANT_KEYS`.

## Lucille's brains: keys and offline models (1.7)

**Config -> "Lucille's brains"** in the phone app (web page: Connect -> "Lucille's brains") is the AI window:

- **Keys** for **OpenRouter** and **Claude (Anthropic)**. Paste, Save, Test, Remove. The key is sent to the PC once and the field is emptied;
  the PC keeps it in `~/.config/lucille-assistant/keys.env` (mode 0600, written atomically; override the path with
  `LUCILLE_ASSISTANT_KEYS`) and uses it immediately, no restart. A key saved here wins over the same key in her `.env`; Remove deletes only the
  copy saved here. **The PC never sends a key back**: the window learns only "set / saved here or in .env / last four characters".
  Test asks the provider a free question (OpenRouter `GET /key`, Anthropic `GET /v1/models`), never a paid chat call. A key is refused if it has
  whitespace, quotes, a backslash or a control character (so a pasted newline cannot add a `SWARM_AUTH_TOKEN=` line to the file).
- **Where she answers from** (`/api/ai/prefs`, stored in the data dir): **Auto** = cloud first, then the offline models, then the canned line ·
  **Cloud only** · **Offline only** (nothing leaves the PC). With both keys set, **Ask first** picks OpenRouter or Claude. NVIDIA NIM
  (`NVIDIA_API_KEY`) is still tried after those two.
- **Offline swarm**: three small models on this PC, reached through any OpenAI-compatible server (Ollama by default,
  `http://127.0.0.1:11434/v1`; llama.cpp's `llama-server` works too). The window shows whether the server answers and which of the three are
  installed, and prints the `ollama pull` line for any that are not. **This program downloads and starts nothing.** Defaults are sized for a PC with
  no GPU and ~8 GB free: `llama3.2:3b` (Steady), `qwen2.5:3b` (Sharp), `gemma3:1b` (Quick), about 4.5 GB on disk together. Edit
  `local_models` in `~/.config/lucille-assistant/config.json` (it is deliberately not editable from the phone, so a phone cannot point her
  conversations at another host):

  ```json
  "local_models": {
    "enabled": true,
    "endpoint": "http://127.0.0.1:11434/v1",
    "mode": "chain",
    "timeout_seconds": 75,
    "models": [
      {"name": "Steady", "model": "llama3.2:3b"},
      {"name": "Sharp",  "model": "qwen2.5:3b"},
      {"name": "Quick",  "model": "gemma3:1b"}
    ]
  }
  ```

  `chain` (default) tries them in order and the first good reply wins, so only one loads at a time. `race` asks all three at once and takes the first
  reply; on 8 CPU cores that can be slower than chain. Keep the model warm with `OLLAMA_KEEP_ALIVE` or the first reply after a pause pays the load time.
  Anything inside `<think>…</think>` is dropped.
- **Claude** is called over plain HTTP (the project is stdlib-only; `GOPROXY=off`): `POST /v1/messages`, `x-api-key`, `anthropic-version: 2023-06-01`,
  no sampling parameters, `output_config.effort` = `low`. Default model **`claude-haiku-5-5`** (cheapest current model, in keeping with the limited
  budget); `ANTHROPIC_MODEL` picks another (e.g. `claude-opus-5-5` for quality), `ANTHROPIC_EFFORT` another effort.
- Timing: cloud gets 20 s in total, then the offline models up to `timeout_seconds` (default 75: a 3B model on a laptop CPU such as this PC's i5-10310U makes ~2 tokens/s, so a spoken reply takes 15-40 s; replies are capped at 120 tokens and the offline models get a short persona, because the full persona plus briefing summaries is 800+ tokens, over a minute of reading at that speed), then the canned line; the phone now waits up to 2 minutes for
  a chat reply (was 25 s) so the offline models are not cut off, and the voice has its own 30 s.
- Background jobs (Dr. Boltz challenge generation, the bridge chat, project-brief suggestions) never use the offline swarm: a 1-3B model can return output that parses but is junk. With no cloud key they fall back exactly as before 1.7 (curated challenges, a visible "no language model answered").
- Only a person may use the window: this PC, or a paired phone / the main token. Dr. Boltz's agent, a caller on a token-less service and a revoked
  token get 401/403, and a web page open on this PC is blocked by the same cross-site guard as the other powerful routes.

| Route | Does |
|---|---|
| `GET /api/ai` | keys (status only), prefs, offline-model status, current model names |
| `POST /api/ai/key` | `{provider: "openrouter"\|"anthropic", key}`; empty key removes the saved one |
| `POST /api/ai/key/test` | `{provider}` free validity check: `valid` / `rejected` / `unreachable` / `no_key` |
| `POST /api/ai/prefs` | `{source: auto\|cloud\|local, cloud_first: openrouter\|anthropic}` |

## Voice and persona (1.4.1)

Her replies are spoken by ElevenLabs (`voice.go`), with OpenRouter's `tts-1-hd` as a last resort. She calls you **babe**, never "darling":
the persona is `lucilleSystemPrompt` in `ai.go` (a sultry, close, flirty companion that stays sensual rather than explicit, speaks in
short sentences with no emoji or markdown, and still nags about water, meds and rest), and the offline fallback replies and the app's
canned lines follow the same rules. A test fails if "darling" creeps back into the prompt or a reply.

- **Free ElevenLabs plans cannot use library voices through the API** (HTTP 402 `paid_plan_required`). `21m00Tcm4TlvDq8ikWAM` (Rachel),
  Charlotte, Nicole and whatever was picked in the voice library are all library voices. The default is now **Lily**
  (`pFZP5JQG7iQjIQuC4Bku`, a premade "velvety" voice that works on every plan). Tried in order: `ELEVEN_VOICE_ID`, Lily, Jessica
  (`cgSgspJ2msm6clMCkdW9`, playful), Sarah, Laura. A voice the account is refused is skipped until the service restarts, with one log
  line saying why (`journalctl --user -u lucille-assistant | grep tts:`). Add $5 of ElevenLabs credit (or a Starter plan) and the configured
  library voice starts working on the next restart, no code change.
- What is spoken is cleaned first (`speakable`): links, markdown, emoji and symbols are dropped, and a reply is cut at a sentence end
  after 500 characters, so one long answer cannot eat the monthly quota. The text on screen is never cut.
- Voice settings are tuned for intimacy: stability 0.35, style 0.5, speaker boost on, speed 0.92 (`voiceSettings()`).
- **If the phone shows canned replies and never speaks, it is almost always unpaired**: the PC refuses chat from a phone with no token
  (HTTP 401), and older apps hid that behind a canned line with no audio. From 1.4.1 the app says it is not paired. Run
  `lucille-assistant pair` and type the code under Settings -> Pair.

## Phone

1. Run as a service: `packaging/install.sh` (listens on loopback only by default).
2. To reach it from a phone, set `LUCILLE_ASSISTANT_LISTEN="<tailscale-ip>:8403;127.0.0.1:8403"` and a real `SWARM_AUTH_TOKEN` in `~/.config/lucille-assistant/.env`. Put the Tailscale address **first**: every address except the last is retried in the background, so the service still starts if the tailnet is not up yet, while the last one (loopback) must bind.
   **This PC's Tailscale address is `100.82.1.19`** (the Debian dev PC; it was `100.82.1.21` until 2026-10-09 and `.21` no longer exists). Never hardcode it, `tailscale ip -4` prints the current one.
3. On the phone (on the tailnet) open `http://<tailscale-ip>:8403/?token=YOUR_TOKEN` once — the token is saved — then *Add to Home screen*. Or install the APK from `dist/`.

### Phone app (`com.lucille.app`, 1.6.0)

- **Settings -> Pair with this PC**: run `lucille-assistant pair` on the PC, type the 6-digit code it prints. The returned per-device
  token is saved the same way the server address is; the raw `SWARM_AUTH_TOKEN` field moved under "Advanced" for anyone who'd rather
  type it by hand.
- **Telemetry** (swipe in from the right edge, or tap the small ‹ tab there): live CPU, RAM, swap, disk, load, temperature, uptime and
  network for this PC and every paired node, over one SSE connection that runs only while the drawer is open. The same drawer's
  **PC actions** section runs the allowlist above, with a second tap required for anything destructive. Swipe right, tap outside or
  press Back to close.
- **Briefs** (📋 in the dock) has two views. **🧾 Brief** is the project brief above (PC live, from git, or cached). **📋 Docs** lists, opens, copies, edits, creates and deletes the briefings above. It is a view of the PC's copy;
  nothing is stored on the phone.
- **Wellness** (🏠 dock, Wellness tab) shows and logs real meds/water/meals/sleep via `/api/care` — no more static demo numbers.
- **Config -> PC address** is saved on the phone. Use `http://127.0.0.1:8403` over USB, which also needs `adb reverse tcp:8403 tcp:8403`
  for each phone (the forward is lost whenever the phone is unplugged or adb restarts, and the app then says it cannot reach the PC),
  or `http://100.82.1.19:8403` over Tailscale.
- If that address does not answer, the app tries `127.0.0.1:8403`, `100.82.1.19:8403` and `10.0.2.2:8403` (the emulator's host) in that
  order. A PC running Lucille Assistant older than 1.1/1.2/1.3 answers HTTP 404 for briefings/telemetry/actions respectively, and the
  app says so.
- Install or update: `adb -s <serial> install -r /mnt/lucillebuild/dist/LucilleAssistant-1.6.0-debug.apk` (same debug key, so it
  updates in place).

Next step: a native Lucille tab in `android_companion` that calls the same API.

## Artifact

`web/index.html` is also the published artifact. With no server reachable it shows clearly-labelled example data.

## Comments, schedule and Dr. I30LTZ chat (2026-10-14, swarm-claude)

- **Comment box under every suggestion** (web Brief tab → *Suggestions & schedule*; logic in `comments.go`, page code in
  `web/assets/lucille-extras.js`). `GET /api/comments` returns every commented suggestion plus the schedule;
  `POST /api/comments/{sg-id} {text, project, title}` stores the comment (people only, `comments.json` 0600 in the data dir)
  and reads plain scheduling words, deterministically, no model call: `done`, `skip`, `later`, `undo`, `tomorrow`,
  `tonight`, `friday 3pm`, `at 2`, `in 2 days`, `in 3 hours`, `next week`. Anything else is kept as a note. The reply
  says what changed ("scheduled for Fri Oct 16, 3:00 PM"). Done and skipped items leave the schedule.
- **Dr. I30LTZ in the chat panel** (switch in the chat header; `persona.go`): `POST /api/chat/i30ltz {message, history}`.
  His own prompt plus an excerpt of his rules, read-only from `/mnt/recoverybuild/60-brain/dr_bolt_rules.md`
  (`LUCILLE_I30LTZ_RULES` overrides), plus the 3 newest shared-memory decisions (MESH-04: shared knowledge,
  never shared persona/model). **Local models only**: no cloud call is ever made for him; if no offline model answers,
  he says so. "Thinking" models (qwen3, deepseek-r1) are tried last: through the OpenAI-compatible endpoint qwen3:4b
  spent its whole budget thinking and returned empty text. Measured: ~30 s per answer on this CPU with llama3.2:3b.
- The page now says "Connecting…" (not "Example data") until its first load finishes.
- Not done: the native Android app does not show the comment box or the Dr. I30LTZ switch yet (the phone's browser at
  `http://100.82.1.19:8403` does); the cloud briefings do not read the comments.
