package server

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ConstantEq compares two strings in constant time.
func ConstantEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// HashPassword hashes with bcrypt.
func HashPassword(p string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(p), 12)
	return string(b), err
}

// CheckPassword verifies a bcrypt hash.
func CheckPassword(hash, p string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}

// ValidatePassword enforces a minimum policy.
func ValidatePassword(p string) error {
	if len(p) < 12 {
		return fmt.Errorf("password must be at least 12 characters")
	}
	if len(p) > 128 {
		return fmt.Errorf("password must be at most 128 characters")
	}
	return nil
}

// TOTP implements RFC 6238 (SHA-1, 30 s, 6 digits) as used by common
// authenticator apps.
func TOTP(secret string, step int64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", code), nil
}

// VerifyTOTP checks code against the current step ±1 and returns the
// matching step so it can be marked used.
func VerifyTOTP(secret, code string, at time.Time) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}
	cur := at.Unix() / 30
	for _, s := range []int64{cur - 1, cur, cur + 1} {
		want, err := TOTP(secret, s)
		if err == nil && ConstantEq(want, code) {
			return s, true
		}
	}
	return 0, false
}

// OTPAuthURI builds the provisioning URI shown as a QR code.
func OTPAuthURI(issuer, account, secret string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

// pending holds half-finished logins (password ok, MFA not yet).
type pending struct {
	userID  int64
	expires time.Time
	enroll  string // new TOTP secret when the user is enrolling
	tries   int
}

type pendingStore struct {
	mu sync.Mutex
	m  map[string]*pending
}

func (p *pendingStore) put(v *pending) string {
	tok := RandomToken(24)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[string]*pending{}
	}
	for k, x := range p.m {
		if time.Now().After(x.expires) {
			delete(p.m, k)
		}
	}
	p.m[tok] = v
	return tok
}

func (p *pendingStore) get(tok string) *pending {
	p.mu.Lock()
	defer p.mu.Unlock()
	v := p.m[tok]
	if v == nil || time.Now().After(v.expires) {
		delete(p.m, tok)
		return nil
	}
	return v
}

func (p *pendingStore) del(tok string) {
	p.mu.Lock()
	delete(p.m, tok)
	p.mu.Unlock()
}

// ipLimiter throttles login attempts per source address.
type ipLimiter struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

func (l *ipLimiter) allow(ip string, max int, per time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string][]time.Time{}
	}
	cut := time.Now().Add(-per)
	keep := l.m[ip][:0]
	for _, t := range l.m[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= max {
		l.m[ip] = keep
		return false
	}
	l.m[ip] = append(keep, time.Now())
	return true
}
