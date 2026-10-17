Lucille Assistant - standalone Linux bundle
===========================================
Contents: lucille-assistant (static binary, web UI embedded), install.sh, uninstall.sh,
lucille-assistant.service, env.example, SHA256SUMS.

It needs nothing else: no Go, no source tree, no SWARM OS, no other drive. Optional
integrations (Google Workspace on :8399, GamingAgent on :8401, the `swarm-memory` CLI,
a shared-memory folder) are used when present and skipped when not.

  ./install.sh
  systemctl --user enable --now lucille-assistant
  open http://127.0.0.1:8403

Keys go in ~/.config/lucille-assistant/.env (never in this bundle).
Phone app: install the matching LucilleAssistant-*.apk, point it at this machine's address.
