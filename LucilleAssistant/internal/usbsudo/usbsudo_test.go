package usbsudo

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

var rfcSecret = []byte("12345678901234567890") // RFC 6238 appendix B (SHA-1)

func TestCodeMatchesRFC6238Vectors(t *testing.T) {
	for _, c := range []struct {
		unix int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		if got := Code(rfcSecret, time.Unix(c.unix, 0)); got != c.want {
			t.Errorf("t=%d: got %s want %s", c.unix, got, c.want)
		}
	}
}

func TestDecodeSecret(t *testing.T) {
	b, err := DecodeSecret("gezd gnbv-gy3t qojq gezd gnbv gy3t qojq") // "1234567890" x2 in base32, as an app shows it
	if err != nil || string(b) != "12345678901234567890" {
		t.Fatalf("got %q %v", b, err)
	}
	if _, err := DecodeSecret("not base32 !!"); err == nil {
		t.Fatal("garbage accepted")
	}
	if _, err := DecodeSecret("MFRGG"); err == nil {
		t.Fatal("too-short secret accepted")
	}
}

func TestAcceptWindowAndReplay(t *testing.T) {
	state := filepath.Join(t.TempDir(), "run", "last")
	now := time.Unix(1111111109, 0)
	code := Code(rfcSecret, now)
	if Accept(rfcSecret, "000000", now, state) {
		t.Fatal("wrong code accepted")
	}
	if Accept(rfcSecret, code[:5], now, state) || Accept(rfcSecret, "", now, state) {
		t.Fatal("short code accepted")
	}
	if !Accept(rfcSecret, code, now, state) {
		t.Fatal("right code refused")
	}
	if Accept(rfcSecret, code, now, state) {
		t.Fatal("the same code worked twice")
	}
	// the previous step is now older than the last one used: refused. The next step is fine (clock drift).
	if Accept(rfcSecret, Code(rfcSecret, now.Add(-Step*time.Second)), now, state) {
		t.Fatal("an older code worked after a newer one")
	}
	if !Accept(rfcSecret, Code(rfcSecret, now.Add(Step*time.Second)), now, state) {
		t.Fatal("the next step (drift) refused")
	}
	if Accept(rfcSecret, Code(rfcSecret, now.Add(5*Step*time.Second)), now, state) {
		t.Fatal("a code from far in the future worked")
	}
}

func writeDev(t *testing.T, root, name, vid, serial string) {
	t.Helper()
	d := filepath.Join(root, name)
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "idVendor"), []byte(vid+"\n"), 0o644)
	if serial != "" {
		os.WriteFile(filepath.Join(d, "serial"), []byte(serial+"\n"), 0o644)
	}
}

func TestPresentMatchesVendorAndSerialOnly(t *testing.T) {
	allow, err := ParseAllow("# my phones\nphone 04E8 R5GYC58ZJCA  Samsung A17\nphone 04e8 RZCY31RFGVX   # a16\nphone 2207 A67LGEN2M202607053035 Revoview\n")
	if err != nil || len(allow) != 3 || allow[0].Vendor != "04e8" || allow[0].Label != "Samsung A17" || allow[1].Label != "RZCY31RFGVX" {
		t.Fatalf("parse: %v %+v", err, allow)
	}
	root := t.TempDir()
	writeDev(t, root, "1-3", "346d", "3664251034070005592") // a random disk
	writeDev(t, root, "2-1", "04e8", "SOMEONEELSE")         // right vendor, wrong serial
	writeDev(t, root, "2-2", "9999", "R5GYC58ZJCA")         // right serial, wrong vendor
	writeDev(t, root, "2-3", "04e8", "")                    // no serial at all
	if _, ok := Present(root, allow); ok {
		t.Fatal("matched a device that is not on the list")
	}
	writeDev(t, root, "3-2", "04e8", "R5GYC58ZJCA")
	d, ok := Present(root, allow)
	if !ok || d.Label != "Samsung A17" {
		t.Fatalf("plugged-in phone not found: %+v %v", d, ok)
	}
	if _, ok := Present(filepath.Join(root, "missing"), allow); ok {
		t.Fatal("matched with no USB tree")
	}
}

func TestParseAllowRejectsJunkAndEmpty(t *testing.T) {
	for _, s := range []string{"", "# only a comment\n", "tablet 04e8 X\n", "phone 04e8\n"} {
		if _, err := ParseAllow(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}

func TestCheckSecretFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s")
	os.WriteFile(p, []byte("x"), 0o600)
	owner := func(os.FileInfo) (uint32, bool) { return 0, true }
	if err := CheckSecretFile(p, 0, owner); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, 0o644)
	if CheckSecretFile(p, 0, owner) == nil {
		t.Fatal("world-readable secret accepted")
	}
	os.Chmod(p, 0o600)
	if CheckSecretFile(p, 0, func(os.FileInfo) (uint32, bool) { return 1000, true }) == nil {
		t.Fatal("user-owned secret accepted")
	}
}
