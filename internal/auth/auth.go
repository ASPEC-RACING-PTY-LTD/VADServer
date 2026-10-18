// Package auth provides password hashing, token generation, rate limiting
// and replay protection.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/time/rate"
)

// Argon2id parameters (OWASP recommendation: m=19 MiB, t=2, p=1).
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16
)

// hashSem bounds concurrent hashing so login floods cannot exhaust memory.
var hashSem = make(chan struct{}, 4)

// HashPassword returns an encoded Argon2id hash.
func HashPassword(ctx context.Context, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	select {
	case hashSem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	<-hashSem
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// dummyHash is verified against when a user does not exist so response
// timing does not reveal valid usernames.
var dummyHash = func() string {
	h, err := HashPassword(context.Background(), RandomToken(24))
	if err != nil {
		panic(err)
	}
	return h
}()

// VerifyPassword checks a password against an encoded hash.
func VerifyPassword(ctx context.Context, password, encoded string) (bool, error) {
	if encoded == "" {
		encoded = dummyHash
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("unsupported password hash")
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	select {
	case hashSem <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	<-hashSem
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// ValidatePassword enforces a minimal password policy.
func ValidatePassword(pw string) error {
	if len(pw) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	if len(pw) > 256 {
		return errors.New("password must be at most 256 characters")
	}
	return nil
}

// RandomToken returns n random bytes as base64url.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken returns the hex SHA-256 of a bearer token, used as the stored id.
func HashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

// UserCode returns a short human-comparable code like "K7QF-4MZ2".
func UserCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	out := make([]byte, 0, 9)
	for i, x := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, alphabet[int(x)%len(alphabet)])
	}
	return string(out)
}

// --- rate limiting ---

// Limiter is a keyed token-bucket limiter with idle eviction.
type Limiter struct {
	mu    sync.Mutex
	r     rate.Limit
	burst int
	m     map[string]*limEntry
}

type limEntry struct {
	l    *rate.Limiter
	seen time.Time
}

func NewLimiter(perMinute float64, burst int) *Limiter {
	l := &Limiter{r: rate.Limit(perMinute / 60), burst: burst, m: map[string]*limEntry{}}
	go func() {
		for range time.Tick(5 * time.Minute) {
			l.mu.Lock()
			for k, e := range l.m {
				if time.Since(e.seen) > 30*time.Minute {
					delete(l.m, k)
				}
			}
			l.mu.Unlock()
		}
	}()
	return l
}

// Allow consumes a token for key.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[key]
	if !ok {
		e = &limEntry{l: rate.NewLimiter(l.r, l.burst)}
		l.m[key] = e
	}
	e.seen = time.Now()
	return e.l.Allow()
}

// --- failed login tracking ---

// FailureTracker counts failed logins per key and blocks after a threshold.
type FailureTracker struct {
	mu        sync.Mutex
	m         map[string]*failEntry
	threshold int
	window    time.Duration
	block     time.Duration
}

type failEntry struct {
	count        int
	first        time.Time
	blockedUntil time.Time
}

func NewFailureTracker(threshold int, window, block time.Duration) *FailureTracker {
	return &FailureTracker{m: map[string]*failEntry{}, threshold: threshold, window: window, block: block}
}

// Blocked reports whether key is currently blocked and for how long.
func (f *FailureTracker) Blocked(key string) (bool, time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.m[key]
	if e == nil {
		return false, 0
	}
	if d := time.Until(e.blockedUntil); d > 0 {
		return true, d
	}
	return false, 0
}

func (f *FailureTracker) Fail(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.m[key]
	now := time.Now()
	if e == nil || now.Sub(e.first) > f.window {
		e = &failEntry{first: now}
		f.m[key] = e
	}
	e.count++
	if e.count >= f.threshold {
		e.blockedUntil = now.Add(f.block)
		e.count = 0
		e.first = now
	}
	if len(f.m) > 100000 {
		for k, v := range f.m {
			if now.Sub(v.first) > f.window && now.After(v.blockedUntil) {
				delete(f.m, k)
			}
		}
	}
}

func (f *FailureTracker) Reset(key string) {
	f.mu.Lock()
	delete(f.m, key)
	f.mu.Unlock()
}

// --- nonce cache ---

// NonceCache remembers request nonces for the signature validity window.
// Combined with the server start-time guard it prevents replays.
type NonceCache struct {
	mu      sync.Mutex
	seen    map[string]time.Time
	ttl     time.Duration
	Started time.Time
}

func NewNonceCache(ttl time.Duration) *NonceCache {
	c := &NonceCache{seen: map[string]time.Time{}, ttl: ttl, Started: time.Now()}
	go func() {
		for range time.Tick(time.Minute) {
			c.mu.Lock()
			now := time.Now()
			for k, t := range c.seen {
				if now.Sub(t) > c.ttl {
					delete(c.seen, k)
				}
			}
			c.mu.Unlock()
		}
	}()
	return c
}

// Use records a nonce and returns false if it was seen before or if the
// request predates this process (nonces from before a restart are unknown).
func (c *NonceCache) Use(key string, reqTime time.Time) bool {
	if reqTime.Before(c.Started.Add(-2 * time.Second)) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.seen[key]; ok {
		return false
	}
	c.seen[key] = time.Now()
	return true
}
