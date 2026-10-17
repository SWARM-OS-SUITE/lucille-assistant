---
title: SWARM OS
project: SW
order: 1
summary: YELLOW 10/9: installed on the NVMe, standalone audit 0 FAIL; the swarm-ui crash fix is written but not yet ISO-built or boot-tested. Next: Sat 9 AM sudo patch + unplugged-drive boot test, then new API keys. 3 suggestions inside.
read_first: /mnt/Lucy/SWARM_AI_FEATURES.md | /mnt/recoverybuild/00-CURRENT/MASTER_BUILD_LIST.md | /mnt/Lucy/CLAUDE.md | /mnt/Lucy/AGENTS.md
---
STATUS (Fri 2026-10-09, 8:45 PM) - YELLOW
- Installed on the internal NVMe and audited: check_swarm_standalone.sh reports 0 FAIL. Only optional references to /mnt/Lucy, /mnt/brains, Ventoy and the Debian GRUB entry remain.
- Fixes are written into the image sources (swarm-ui.service logging, workspace-parity skip, stale fstab lines) but NOT rebuilt into an ISO or boot-tested. The swarm-ui.service silent crash (since 09-15) is not confirmed fixed.
- Waiting on you: the sudo patch + unplugged-drive boot test, and new OpenRouter + Steam keys (the dead OpenRouter key breaks every /api/lucille/* call).
- /mnt/Lucy has 13 uncommitted files (auto-sync is running).
- LucyServer :5000 is up on loopback and 100.82.1.19, and a phone is syncing memory with it.

NEXT STEPS & SCHEDULE (proposed)
[YOU] = needs your hands: sudo, hardware, keys, decisions. [AI] = ask an AI session that day.
1. Sat 10/10  9:00 AM [YOU] 20 min: sudo 70-scripts/check_swarm_standalone.sh, then make_installed_swarm_standalone.sh (patches the installed root; only tested on a copy).
2. Sat 10/10  9:30 AM [YOU] 30 min: boot from the NVMe with every external drive unplugged; note whether swarm-ui.service stays up.
3. Sat 10/10 11:00 AM [YOU] 10 min: regenerate OPENROUTER_API_KEY and STEAM_API_KEY at the providers (SW-04) and paste them into 20-runtime/.env, never into chat or git.
4. Sun 10/11  [AI] 1 h: secret-scan and commit the 13 pending files; fix the rsync --no-delete bug in sync_lucille_ai.sh.
5. Mon 10/12  [AI] 1 h: rebuild the ISO from /mnt/swarmbuild and Ventoy-boot it to prove the swarm-ui fix (SW-01, SW-03).
6. Wed 10/14  [AI] 1.5 h: GamingAgent fixes (SW-05: HTTP status checks, a real /health) with tests, then ISO .sha256 files (SW-18).

3 SUGGESTIONS
1. Boot-health chip: show green / amber / red on the desktop from one /api/health (SW-19), so a silent swarm-ui crash cannot go unseen again. About half a day.
2. Unplugged-drive canary: a boot-time job that logs which external mounts were missing and that the desktop still came up, turning "runs off the NVMe alone" into a nightly proof. About 2 h.
3. ISO release gate: one script that refuses to copy an ISO to Ventoy unless the VM boot test passed and the .sha256 exists (needs qemu-utils, SW-10). About 3 h.

REFERENCE
- /mnt/Lucy (capital L) is the source of truth. ISOs build on /mnt/swarmbuild and go only to /mnt/ventoy/ISO/SWARM_OS/ (newest 5 kept). Never write to lucy-master or /mnt/lucy.
- Every session: check_mounts.sh, read SWARM_AI_FEATURES.md, claim your area with ai-board.sh, update the tracker when done.
- Never commit .env. Do not touch 30-source/GamingAgent (old .NET) or lucille-core.
- Ports: GamingAgent 8401, LucyServer 5000, GoogleWorkspaceService 8399. Arch + archiso, Flutter shell + Slint panel under labwc.
