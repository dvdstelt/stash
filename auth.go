package main

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

/* ---------- Passwords ---------- */

const pbkdf2Iterations = 600_000 // OWASP's recommendation for PBKDF2-HMAC-SHA256

func hashPassword(password string) (string, error) {
	salt := randomBytes(16)
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

func checkPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 {
		return false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[2])
	want, err2 := enc.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

func tokenEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

/* ---------- Owner sessions ---------- */

const (
	sessionCookie   = "stash_session"
	sessionLifetime = 30 * 24 * time.Hour
)

// sessions keeps signed-in browsers in memory, keyed by a hash of the cookie value.
// A restart signs everyone out, which is fine for a single owner.
type sessions struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *sessions) create() (token string, expires time.Time) {
	token = base64.RawURLEncoding.EncodeToString(randomBytes(32))
	expires = time.Now().Add(sessionLifetime)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[sessionKey(token)] = expires
	return token, expires
}

func (s *sessions) valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.m[sessionKey(token)]
	return ok && time.Now().Before(expires)
}

func (s *sessions) remove(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, sessionKey(token))
}

func (s *sessions) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, expires := range s.m {
		if time.Now().After(expires) {
			delete(s.m, k)
		}
	}
}

func (srv *server) signedIn(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && srv.sessions.valid(c.Value)
}

/* ---------- Unlocked password-protected links ---------- */

// unlockValue is the cookie value proving someone entered a link's password. It is tied to the
// password hash, so changing the password locks everyone out again.
func (srv *server) unlockValue(kind, id, passwordHash string) string {
	mac := hmac.New(sha256.New, srv.st.secret)
	fmt.Fprintf(mac, "%s\x00%s\x00%s", kind, id, passwordHash)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func unlockCookieName(kind, id string) string { return "stash_" + kind + "_" + id }

func (srv *server) setUnlocked(w http.ResponseWriter, r *http.Request, kind, id, passwordHash string) {
	http.SetCookie(w, &http.Cookie{
		Name: unlockCookieName(kind, id), Value: srv.unlockValue(kind, id, passwordHash), Path: "/",
		MaxAge: int((7 * 24 * time.Hour).Seconds()), HttpOnly: true, Secure: srv.isHTTPS(r), SameSite: http.SameSiteStrictMode,
	})
}

func (srv *server) isUnlocked(r *http.Request, kind, id, passwordHash string) bool {
	if passwordHash == "" {
		return true
	}
	c, err := r.Cookie(unlockCookieName(kind, id))
	return err == nil && hmac.Equal([]byte(c.Value), []byte(srv.unlockValue(kind, id, passwordHash)))
}

/* ---------- Slowing down password guessing ---------- */

// limiter slows down repeated wrong passwords per client and target: after 5 failures,
// each further attempt has to wait twice as long as the last, up to 15 minutes.
type limiter struct {
	mu    sync.Mutex
	fails map[string]*failures
}

type failures struct {
	count int
	until time.Time
	last  time.Time
}

func (l *limiter) wait(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f := l.fails[key]; f != nil {
		return time.Until(f.until)
	}
	return 0
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.fails[key]
	if f == nil {
		f = &failures{}
		l.fails[key] = f
	}
	f.count++
	f.last = time.Now()
	if f.count >= 5 {
		delay := time.Second << min(f.count-5, 10)
		f.until = time.Now().Add(min(delay, 15*time.Minute))
	}
}

func (l *limiter) succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

func (l *limiter) prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, f := range l.fails {
		if time.Since(f.last) > time.Hour && time.Now().After(f.until) {
			delete(l.fails, k)
		}
	}
}

/* ---------- Client address behind a reverse proxy ---------- */

func (srv *server) trusted(addr netip.Addr) bool {
	for _, p := range srv.cfg.TrustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func remoteAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, _ := netip.ParseAddr(host)
	return addr.Unmap()
}

// clientIP only believes X-Forwarded-For when the request comes from a configured proxy, and then
// takes the right-most address that isn't one of those proxies. Anyone else can put anything in
// that header, so trusting it blindly would let them dodge the password limits.
func (srv *server) clientIP(r *http.Request) netip.Addr {
	addr := remoteAddr(r)
	if !srv.trusted(addr) {
		return addr
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		hop = hop.Unmap()
		if !srv.trusted(hop) {
			return hop
		}
		addr = hop
	}
	return addr
}

func (srv *server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return srv.trusted(remoteAddr(r)) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// origin is the scheme and host people should use for links, such as https://files.example.com.
func (srv *server) origin(r *http.Request) string {
	if srv.cfg.PublicURL != "" {
		return srv.cfg.PublicURL
	}
	scheme := "http"
	if srv.isHTTPS(r) {
		scheme = "https"
	}
	host := r.Host
	if srv.trusted(remoteAddr(r)) {
		if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
			host = fwd
		}
	}
	return scheme + "://" + host
}
