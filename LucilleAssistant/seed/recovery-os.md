---
title: I30LTZ Recovery OS
project: RC
order: 2
summary: YELLOW 10/9: installed to disk with its own bootloader but never booted. Next: Sat 10:15 AM root password, then boot it with the Debian drive unplugged. Open: login user, Dr. I30LTZ rules (0 bytes) and the 7B model. 3 suggestions inside.
read_first: /mnt/recoverybuild/00-CURRENT/MASTER_BUILD_LIST.md | /mnt/recoverybuild/CLAUDE.md | /mnt/recoverybuild/00-CURRENT/AI_INSTRUCTIONS.md | /mnt/recoverybuild/00-CURRENT/MOUNTS.md
---
STATUS (Fri 2026-10-09, 8:45 PM) - YELLOW
- Installed to disk today (you ran install-recovery-from-debian.sh): sda3 btrfs "Recovery OS", its own bootloader on ESP sda1, and a firmware entry added after Debian. It has not been booted yet. The installer's chroot is still mounted at /mnt/recoveryos-install, so its last stage is probably waiting on you.
- Built on Debian 13 (trixie): the first ISO (0.3.0-alpha) built OK on 10-07.
- Open on the install: the root password stage, a boot test with the Debian drive unplugged, no login user besides root, and Debian's /etc/fstab still pins the old /mnt/recoveryos UUID.
- Not started: dr_bolt_rules.md is still 0 bytes (RC-03) and the 7B model is not staged (RC-04), so Dr. I30LTZ has no rules or brain on the image yet.

NEXT STEPS & SCHEDULE (proposed)
[YOU] = needs your hands: sudo, hardware, keys, decisions. [AI] = ask an AI session that day.
1. Sat 10/10 10:15 AM [YOU] 10 min: finish the installer's interactive root-password stage, then let it unmount.
2. Sat 10/10 10:30 AM [YOU] 20 min: boot it with the Debian drive unplugged (F12, pick the Recovery disk) and tell me what you see.
3. Sat 10/10  [YOU] 2 min: point Debian's /etc/fstab /mnt/recoveryos line at UUID 71287bbf-81bf-4074-9619-4180b5a15742, then systemctl daemon-reload.
4. Sun 10/11  [AI] 1 h: add a normal login user to the installer and dry-run it (RC-11).
5. Mon 10/12  [AI] 2 h: write 60-brain/dr_bolt_rules.md (RC-03); stage the 7B model and Ollama (RC-04).
6. Tue 10/13  [AI] 1 h: decide ClamAV bundling (about 115 MB offline database) and add it to the ISO profile (CP-20); rebuild and run test-iso-vm.sh.

3 SUGGESTIONS
1. 60-second boot self-check: on first boot print one PASS / FAIL screen (disks listed read-only, Ollama up, model loaded, network off). It makes "zero external drives" a checkable claim.
2. Case file per repair: every repair writes a plain-text log (findings, what you approved, SMART before and after) to the external drive, and the phone app can show the history.
3. Stick self-verify: on boot, check the image against a shipped .sha256 and warn on a mismatch; a flaky USB stick is the usual cause of "it will not boot".

REFERENCE
- Debian 13 live-build. The AI is Dr. I30LTZ ("MAD IT"): Ollama, 7B max, integrated GPU only. It must boot and repair with zero external drives and no cloud.
- Build root /mnt/recoverybuild (sudo bash 70-scripts/build.sh); installed root /mnt/recoveryos. ISOs go to /mnt/ventoy/ISO/recovery-os/ (newest 5 kept).
- Product map (you, 2026-10-09): A SWARM OS and B Recovery OS are the operating systems; C Lucille Assistant and D Boltz Recovery are the phone apps.
- Rules: no secrets anywhere; no destructive action without the exact target from you; every AI action needs your approval; claim your area with ai-board.sh first.
