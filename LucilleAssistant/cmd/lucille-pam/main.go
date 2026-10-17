// lucille-pam is the root-owned half of phone-approved sudo. It is invoked by pam_exec.so
// as root (the same privilege sudo's own PAM transaction already runs with, before it
// drops to the target user), so it — not the Lucille service, which runs as an ordinary
// user — is the one piece of this feature that must be trusted.
//
// It talks to the Lucille service only as a dumb carrier (see internal/unlockproto and
// unlock.go's comment for the trust model) and makes every real decision itself: the
// nonce, the 4-digit code, and whether the phone's signature actually verifies against
// /etc/lucille/approver.pub. Any failure — wrong answer, no answer, service unreachable,
// bad key file — exits non-zero, which a `sufficient` line in /etc/pam.d/sudo falls
// through from straight to the password prompt. This binary can therefore block an
// unlock; it can never force one through, and it can never turn a bad outcome into a
// lockout, because the password path never depends on it.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"swarm-os/lucille-assistant/internal/unlockproto"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	os.Exit(run())
}

func run() int {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	service := env("PAM_SERVICE", "unknown")
	user := env("PAM_USER", "")
	if user == "" {
		fmt.Fprintln(os.Stderr, "lucille-pam: PAM_USER is not set; pam_exec should provide it")
		return 1
	}

	cfg := unlockproto.Config{
		API:        env("LUCILLE_UNLOCK_API", "http://127.0.0.1:8403"),
		PubKeyPath: env("LUCILLE_UNLOCK_PUBKEY", "/etc/lucille/approver.pub"),
		StateDir:   env("LUCILLE_UNLOCK_STATE", "/run/lucille-unlock"),
		Service:    service,
		User:       user,
		Host:       host,
		TTL:        45 * time.Second,
		Out:        os.Stdout, // pam_exec's "stdout" option relays this to the person at the keyboard
	}

	// A hard ceiling independent of the TTL, so a misbehaving service can never hang the
	// login prompt indefinitely — this always returns control to PAM.
	ctx, cancel := context.WithTimeout(context.Background(), cfg.TTL+15*time.Second)
	defer cancel()

	if err := unlockproto.Approve(ctx, cfg); err != nil {
		// Deliberately no detail to stderr beyond this: PAM logs are root-readable but
		// often end up in syslog, and the exact failure reason (wrong number vs. no
		// phone vs. service down) is not something to hand to whoever is at the prompt.
		fmt.Fprintln(os.Stderr, "lucille-pam: not approved:", err)
		return 1
	}
	return 0
}
