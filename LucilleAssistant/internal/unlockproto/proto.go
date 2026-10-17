// Package unlockproto is the shared core of "approve sudo from your phone".
//
// Trust model, in one paragraph: the Lucille service (running as the ordinary user) only
// CARRIES messages between the root-owned PAM helper and the phone. It is never believed.
// The helper invents the request, the one-time nonce and the 4-digit code itself, shows the
// code only on the PC screen, and sends the service just three candidate numbers (the code
// plus two decoys). The phone has to pick the matching number and sign
// {id, nonce, chosen, service, user, host, expiry} with a hardware-backed key that needs a
// fingerprint for every use. The helper verifies that signature against a public key in a
// root-owned file. A fake or hijacked service can therefore block an approval, but it can
// never forge one, and a signature from an older request fails because the nonce differs.
package unlockproto

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const Version = "lucille-unlock-v1"

// Request is what the helper hands the service to pass on to the phone. It does NOT
// contain the code: Choices holds the code and two decoys in random order.
type Request struct {
	ID      string   `json:"id"`      // 16 random bytes, hex
	Nonce   string   `json:"nonce"`   // 16 random bytes, hex
	Choices []string `json:"choices"` // three distinct 4-digit numbers, shuffled
	Service string   `json:"service"` // PAM service, e.g. "sudo"
	User    string   `json:"user"`
	Host    string   `json:"host"`
	Expires int64    `json:"expires"` // unix seconds
}

// Response is what the phone sends back. Sig is base64 (std) of an ASN.1/DER ECDSA
// signature over sha256(Message(req, Chosen)).
type Response struct {
	ID     string `json:"id"`
	Chosen string `json:"chosen"`
	Sig    string `json:"sig"`
}

var (
	ErrExpired     = errors.New("request expired")
	ErrWrongID     = errors.New("response is for a different request")
	ErrWrongNumber = errors.New("wrong number chosen")
	ErrBadSig      = errors.New("signature does not verify")
)

// Message is the exact byte string the phone signs. Every field is bound, so a signature
// cannot be reused for another request, number, service, user, host or time window.
func Message(r Request, chosen string) []byte {
	return []byte(strings.Join([]string{
		Version,
		"id=" + r.ID,
		"nonce=" + r.Nonce,
		"chosen=" + chosen,
		"service=" + r.Service,
		"user=" + r.User,
		"host=" + r.Host,
		fmt.Sprintf("expires=%d", r.Expires),
	}, "\n"))
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// randBelow returns a uniform number in [0, n) from crypto/rand.
func randBelow(n int64) (int64, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return 0, err
	}
	return v.Int64(), nil
}

// NewRequest builds a request and returns the code the user must match. The code is the
// only secret that stays on the PC; it is never part of the returned Request except as one
// of three indistinguishable choices.
func NewRequest(service, user, host string, ttl time.Duration, now time.Time) (Request, string, error) {
	for _, s := range []string{service, user, host} {
		if s == "" || strings.ContainsAny(s, "\n\r\x00") {
			return Request{}, "", errors.New("service, user and host must be non-empty single-line values")
		}
	}
	id, err := randHex(16)
	if err != nil {
		return Request{}, "", err
	}
	nonce, err := randHex(16)
	if err != nil {
		return Request{}, "", err
	}
	// Three distinct 4-digit numbers (leading zeros kept), the first is the code.
	nums := make([]string, 0, 3)
	seen := map[string]bool{}
	for len(nums) < 3 {
		n, err := randBelow(10000)
		if err != nil {
			return Request{}, "", err
		}
		s := fmt.Sprintf("%04d", n)
		if !seen[s] {
			seen[s] = true
			nums = append(nums, s)
		}
	}
	code := nums[0]
	// Fisher-Yates so the code's position tells the carrier nothing.
	for i := len(nums) - 1; i > 0; i-- {
		j, err := randBelow(int64(i + 1))
		if err != nil {
			return Request{}, "", err
		}
		nums[i], nums[j] = nums[j], nums[i]
	}
	return Request{
		ID: id, Nonce: nonce, Choices: nums,
		Service: service, User: user, Host: host,
		Expires: now.Add(ttl).Unix(),
	}, code, nil
}

// ParsePublicKey reads a PEM "PUBLIC KEY" block (X.509 SubjectPublicKeyInfo, what Android's
// PublicKey.getEncoded() produces) and requires an ECDSA P-256 key.
func ParsePublicKey(pemBytes []byte) (*ecdsa.PublicKey, error) {
	blk, _ := pem.Decode(pemBytes)
	if blk == nil || blk.Type != "PUBLIC KEY" {
		return nil, errors.New("no PUBLIC KEY block found")
	}
	k, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(*ecdsa.PublicKey)
	if !ok || pub.Curve.Params().Name != "P-256" {
		return nil, errors.New("approver key must be ECDSA P-256")
	}
	return pub, nil
}

// PublicKeyPEM is the inverse of ParsePublicKey, for storing what the phone enrols.
func PublicKeyPEM(spkiDER []byte) ([]byte, error) {
	if _, err := ParsePublicKey(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spkiDER})); err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spkiDER}), nil
}

// Check is the one decision the helper makes. It returns nil only if the response is for
// this request, is in time, names the number shown on the PC, and carries a valid
// signature from the enrolled phone key.
func Check(pub *ecdsa.PublicKey, req Request, code string, resp Response, now time.Time) error {
	if now.Unix() > req.Expires {
		return ErrExpired
	}
	if subtle.ConstantTimeCompare([]byte(resp.ID), []byte(req.ID)) != 1 {
		return ErrWrongID
	}
	if subtle.ConstantTimeCompare([]byte(resp.Chosen), []byte(code)) != 1 {
		return ErrWrongNumber
	}
	sig, err := base64.StdEncoding.DecodeString(resp.Sig)
	if err != nil {
		return ErrBadSig
	}
	digest := sha256.Sum256(Message(req, resp.Chosen))
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return ErrBadSig
	}
	return nil
}
