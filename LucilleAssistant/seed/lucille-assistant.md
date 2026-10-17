---
title: Lucille Assistant
project: LA
order: 4
summary: YELLOW 10/9: 1.1.0 is live and on all 3 phones (Briefs, wide top card, saved address). Chat stays canned until a new OpenRouter key goes in (Sat 11 AM). Next: Wellness tab on real data, drop the hardcoded 1307. 3 suggestions inside.
read_first: /mnt/lucillebuild/README.md | /mnt/lucillebuild/src/LucilleAssistant/README.md | /mnt/recoverybuild/00-CURRENT/MASTER_BUILD_LIST.md | /mnt/Lucy/SWARM_AI_FEATURES.md
---
STATUS (Fri 2026-10-09, 8:45 PM) - YELLOW
- Live: Lucille Assistant 1.1.0 runs from its stand-alone install (you cut over at 8:08 PM). It listens on 100.82.1.19:8403 and loopback; callers off loopback need the token.
- Phone app 1.1.0 is on all three phones: Briefs tab, wide top card, and it remembers the PC address. USB works on all three; Tailscale works on two (Tailscale is off on the A67 phone).
- LucyServer :5000 is up on the new address and a phone is already syncing memory with it.
- Gaps: chat has no real brain (the OpenRouter key is dead, so replies are canned fallbacks). In the phone app only Chat and Briefs talk to the PC; the Wellness, Plan, OS, Music and Art tabs are still static demo screens. The older lucille-android/ app does not compile (undefined color Void). The source changes are not committed (11 files). The PIN/token 1307 is hardcoded in the service and the app.

NEXT STEPS & SCHEDULE (proposed)
[YOU] = needs your hands: sudo, hardware, keys, decisions. [AI] = ask an AI session that day.
1. Sat 10/10  9:00 AM [YOU] 2 min: turn Tailscale on in the A67 phone (lucille-llcy) so it works off the cable.
2. Sat 10/10 11:00 AM [YOU] 10 min: put a fresh OpenRouter key into ~/.config/lucille-assistant/.env and restart the service (the same key SW-04 needs); chat then answers for real.
3. Sun 10/11  [AI] 30 min: secret-scan and commit /mnt/lucillebuild/src (11 files).
4. Mon 10/12  [AI] 2 h: wire the phone app's Wellness tab to the PC's /api/care (meds, water, meals, sleep) so the app is useful every day.
5. Tue 10/13  [AI] 1 h: replace the hardcoded 1307 with SWARM_AUTH_TOKEN, entered once in Config and kept on the phone.
6. Wed 10/14  [YOU] 1 min: say OK to deleting the old copy under /mnt/Lucy/30-source/LucilleAssistant (deprecated, verified identical).

3 SUGGESTIONS
1. Self-updating status: a nightly job rewrites the STATUS lines of these briefings from git state and the tracker, so they never go stale (today they are a dated snapshot).
2. Morning push: at wake time, send the day's first three scheduled steps as a phone notification (the "Phone push" job on the roadmap).
3. Schedule on the calendar: create these scheduled steps as Google Calendar events through the existing GoogleWorkspaceService, so reminders arrive with the rest of your day.

REFERENCE
- A Go service (stdlib only) on :8403 with a phone web app and an Android app (com.lucille.app). Source /mnt/lucillebuild/src/LucilleAssistant; build with scripts/build.sh; dist/ runs stand-alone.
- Builds only on /mnt/lucillebuild. Built apps must run with no build tree, no /mnt/Lucy and no other drive; no secrets in git or dist/. Android needs JDK 17-21.
- Config in ~/.config/lucille-assistant/ (config.json, .env); data in ~/.local/share/lucille-assistant/. USB phones need adb reverse tcp:8403 tcp:8403, which is lost on unplug.
- The service also holds chat, the care log, git and drive status, mail and calendar, news and deals.
