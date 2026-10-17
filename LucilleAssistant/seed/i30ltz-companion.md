---
title: I30LTZ Companion
project: CP
order: 3
summary: YELLOW 10/9: Boltz Recovery's phone app (D) builds on its own partition; the APK installs as an update but has not been tried on a device. Next: Sat 11:30 AM install on one phone and walk pairing to terminal. 3 suggestions inside.
read_first: /mnt/recoverybuild/00-CURRENT/MASTER_BUILD_LIST.md | /mnt/boltzbuild/README.md | /mnt/recoverybuild/00-CURRENT/AI_INSTRUCTIONS.md
---
STATUS (Fri 2026-10-09, 8:45 PM) - YELLOW
- I30LTZ Companion 6.2.21 (versionCode 81) is the phone app of Boltz Recovery (D). Its source now lives on /mnt/boltzbuild/src/I30LTZ-Recovery-companion and builds there on its own (clean-environment build OK, 35 MB debug APK in /mnt/boltzbuild/dist/).
- That APK has the same signing certificate as the Oct-8 build on your phones, so it installs as an update; the old APK is kept in dist/previous for rollback. It has not been installed or tested on a device yet.
- Working per the tracker (mostly PARTIAL): node list and pairing, node agent i30ltz-node 0.3.x, live telemetry, mirrored tmux terminal, ClamAV scan per node. Phone-as-node passes on the emulator; the voice tests pass but nobody has listened yet.
- Being worked on right now: a known-machines / node-registry change (7 uncommitted files in the repo).
- Waiting on you: delete B's duplicate copy of the app and three stale git worktrees (the AI cannot delete them).

NEXT STEPS & SCHEDULE (proposed)
[YOU] = needs your hands: sudo, hardware, keys, decisions. [AI] = ask an AI session that day.
1. Sat 10/10 10:00 AM [YOU] 5 min: remove the duplicate on B (rm -rf /mnt/recoverybuild/I30LTZ-Recovery-companion, verified identical to D's) and the three stale worktrees named in the SWARM tracker's D note.
2. Sat 10/10 11:30 AM [YOU] 30 min: install /mnt/boltzbuild/dist/I30LTZ-Companion-6.2.21-vc81-debug.apk on one phone and walk Nodes, pair, telemetry, terminal (CP-02, CP-17).
3. Sun 10/11  [YOU] 15 min: listen to Dr. Boltz's voice (CP-24) and try phone-as-node on a real phone (CP-23).
4. Mon 10/12  [AI] 1 h: install contrib/i30ltz-node.service so the agent survives reboots.
5. Tue 10/13  [AI] 2 h: command approvals and AI chat over SSE (CP-15); decide agent-on-Recovery versus bridge endpoints (CP-14).
6. Thu 10/15  [AI] 1.5 h: keep the terminal session when you switch tabs (CP-18).

3 SUGGESTIONS
1. Pairing QR: show a QR (address plus one-time code) on the PC or Recovery screen and scan it with the phone, so there are no tokens to type or paste.
2. Scheduled scans, quiet pushes: a nightly quick ClamAV scan per node, with a phone notification only when something is found (the first slice of CP-21).
3. Approve from the lock screen: Approve / Deny notification actions that still ask for biometric on Approve, so approvals do not need the app open.

REFERENCE
- Android (Kotlin / Compose), applicationId com.aistudio.sysopt.app. Build: scripts/build.sh on /mnt/boltzbuild (JDK 21). Node agent: /mnt/recoverybuild/30-source/i30ltz-node (Go, stdlib only).
- No compiled-in default host or token; nothing runs on a node without your approval (Approve needs biometric); keep MainViewModel's init after every property.
- Not the Lucille app: this one is Dr. I30LTZ's PC-fixing job. Owner: the Recovery AI.
