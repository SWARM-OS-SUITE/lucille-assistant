#!/usr/bin/env bash
# Remove the program and its service unit. Keeps your config, .env and data unless you pass --purge.
set -euo pipefail
PREFIX=${LUCILLE_PREFIX:-$HOME/.local/opt/lucille-assistant}
CFGDIR=${XDG_CONFIG_HOME:-$HOME/.config}
systemctl --user disable --now lucille-assistant 2>/dev/null || true
rm -f "$CFGDIR/systemd/user/lucille-assistant.service"
rm -rf "$PREFIX"
systemctl --user daemon-reload 2>/dev/null || true
if [[ ${1:-} == --purge ]]; then
  rm -rf "$CFGDIR/lucille-assistant" "${XDG_DATA_HOME:-$HOME/.local/share}/lucille-assistant"
  echo "removed program, config, .env and data"
else
  echo "removed program; kept $CFGDIR/lucille-assistant and ~/.local/share/lucille-assistant (use --purge to delete)"
fi
