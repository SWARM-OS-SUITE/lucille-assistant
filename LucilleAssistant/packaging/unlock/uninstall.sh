#!/bin/bash
# Reverts install.sh: removes the marked block from /etc/pam.d/sudo and the binary.
# Your password keeps working exactly as before either script ran.
#
# Run as: sudo bash uninstall.sh
set -euo pipefail

PAM_FILE="${LUCILLE_PAM_FILE_OVERRIDE:-/etc/pam.d/sudo}" # override is for this script's own tests only
MARKER="# lucille-unlock (phone-approved sudo; see packaging/unlock/)"

[ "$(id -u)" = 0 ] || { echo "run with sudo" >&2; exit 1; }

if grep -qF "$MARKER" "$PAM_FILE" 2>/dev/null; then
  cp -a "$PAM_FILE" "$PAM_FILE.bak-$(date +%Y%m%d-%H%M%S)"
  # Removes the marker line and the very next line (the auth line this installed),
  # plus one blank line after it if install.sh's exact layout is still intact.
  awk -v m="$MARKER" '
    $0==m { skip=2; next }
    skip>0 && $0=="" { skip--; next }
    skip>0 { skip--; next }
    { print }
  ' "$PAM_FILE" > "$PAM_FILE.tmp"
  install -m 0644 -o root -g root "$PAM_FILE.tmp" "$PAM_FILE"
  rm -f "$PAM_FILE.tmp"
  echo "removed from $PAM_FILE (backup kept alongside it)"
else
  echo "not installed in $PAM_FILE"
fi

rm -f /usr/local/bin/lucille-pam
echo "done. Verify: sudo -k; sudo true"
