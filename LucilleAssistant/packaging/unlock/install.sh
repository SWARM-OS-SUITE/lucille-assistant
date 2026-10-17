#!/bin/bash
# Installs phone-approved sudo: the lucille-pam binary, and one line in
# /etc/pam.d/sudo ABOVE @include common-auth. "sufficient", never "required": a phone
# that never answers (dead, no network, no key enrolled yet) falls straight through to
# the normal password prompt, exactly as before this script ran. Uninstalling removes
# only that one line and the binary; nothing about your password changes either way.
#
# Run as: sudo bash install.sh
set -euo pipefail

BIN_SRC="${1:-$(dirname "$0")/../../lucille-pam}"
BIN_DST=/usr/local/bin/lucille-pam
PAM_FILE="${LUCILLE_PAM_FILE_OVERRIDE:-/etc/pam.d/sudo}" # override is for this script's own tests only
MARKER="# lucille-unlock (phone-approved sudo; see packaging/unlock/)"
LINE="auth sufficient pam_exec.so quiet stdout $BIN_DST"

[ "$(id -u)" = 0 ] || { echo "run with sudo" >&2; exit 1; }
[ -x "$BIN_SRC" ] || { echo "build lucille-pam first: (cd cmd/lucille-pam && go build -o ../../lucille-pam .)"; exit 1; }
[ -f /etc/lucille/approver.pub ] || echo "NOTE: /etc/lucille/approver.pub does not exist yet — the phone approval step will always fail over to your password until you enrol a phone and install that file. That is safe, not broken."

install -m 0755 -o root -g root "$BIN_SRC" "$BIN_DST"
mkdir -p /run/lucille-unlock
chmod 0700 /run/lucille-unlock

if grep -qF "$MARKER" "$PAM_FILE" 2>/dev/null; then
  echo "already installed in $PAM_FILE"
else
  cp -a "$PAM_FILE" "$PAM_FILE.bak-$(date +%Y%m%d-%H%M%S)"
  tmp="$(mktemp)"
  { printf '%s\n%s\n\n' "$MARKER" "$LINE"; cat "$PAM_FILE"; } > "$tmp"
  install -m 0644 -o root -g root "$tmp" "$PAM_FILE"
  rm -f "$tmp"
  echo "added to $PAM_FILE (backup kept alongside it)"
fi

echo
echo "Test it NOW in a SEPARATE terminal before closing this one:"
echo "    sudo -k; sudo true"
echo "Keep a root shell open in another window until you are sure it still works:"
echo "    sudo -s"
echo "If anything looks wrong: sudo bash $(dirname "$0")/uninstall.sh"
