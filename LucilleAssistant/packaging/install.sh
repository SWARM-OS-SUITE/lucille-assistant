#!/usr/bin/env bash
# Install Lucille Assistant for the current user. No root, no build tree, no network.
#   ./install.sh                       -> ~/.local/opt/lucille-assistant
#   LUCILLE_PREFIX=/some/dir ./install.sh
set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
PREFIX=${LUCILLE_PREFIX:-$HOME/.local/opt/lucille-assistant}
CFGDIR=${XDG_CONFIG_HOME:-$HOME/.config}
CFG=$CFGDIR/lucille-assistant
UNITDIR=$CFGDIR/systemd/user

install -d "$PREFIX" "$CFG" "$UNITDIR"
install -m 755 "$HERE/lucille-assistant" "$PREFIX/lucille-assistant"
if [[ ! -f $CFG/.env ]]; then
  install -m 600 "$HERE/env.example" "$CFG/.env"
  echo "created $CFG/.env  (add your keys there; it is read at start-up)"
fi
sed "s|@PREFIX@|$PREFIX|g; s|@ENVFILE@|$CFG/.env|g" "$HERE/lucille-assistant.service" > "$UNITDIR/lucille-assistant.service"
systemctl --user daemon-reload 2>/dev/null || echo "note: no user systemd session; run $PREFIX/lucille-assistant directly"
cat <<MSG
Installed to $PREFIX
  start now and at login:  systemctl --user enable --now lucille-assistant
  or run in a terminal:    $PREFIX/lucille-assistant
  then open:               http://127.0.0.1:8403
First run writes $CFG/config.json - edit your real med names/times there.
MSG
