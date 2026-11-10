package main

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

/* ---------- Passwords ---------- */

func TestPasswordHashing(t *testing.T) {
	t.Parallel()
	hash, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, fmt.Sprintf("pbkdf2-sha256$%d$", pbkdf2Iterations)) {
		t.Errorf("hash = %q; want PBKDF2-SHA256 with %d rounds", hash, pbkdf2Iterations)
	}
	if ok, err := checkPassword(hash, "correct horse battery"); !ok || err != nil {
		t.Errorf("the right password isn't accepted: %v, %v", ok, err)
	}
	if ok, _ := checkPassword(hash, "correct horse batterY"); ok {
		t.Error("a wrong password is accepted")
	}
	other, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if other == hash {
		t.Error("two hashes of the same password are equal; the salt isn't random")
	}
}

func TestCheckPasswordRejectsBrokenHashes(t *testing.T) {
	t.Parallel()
	for _, hash := range []string{
		"",
		"plain-text-password",
		"md5$1$c2FsdA$a2V5",
		"pbkdf2-sha256$x$c2FsdA$a2V5",
		"pbkdf2-sha256$0$c2FsdA$a2V5",
		"pbkdf2-sha256$-5$c2FsdA$a2V5",
		"pbkdf2-sha256$1000$!!!$a2V5",
		"pbkdf2-sha256$1000$c2FsdA$!!!",
		"pbkdf2-sha256$1000$c2FsdA$a2V5$extra",
	} {
		if ok, err := checkPassword(hash, ""); ok || err != nil {
			t.Errorf("checkPassword(%q) = %v, %v; want false, nil", hash, ok, err)
		}
	}
}

/* ---------- Setting the first password, signing in and out ---------- */

func TestFirstPasswordNeedsTheSetupLink(t *testing.T) {
	t.Parallel()
	e := startServer(t, config{})
	browser := newBrowser()

	var session struct{ SignedIn, SetupRequired bool }
	e.expect(e.send(browser, http.MethodGet, "/api/session", nil), http.StatusOK).decode(t, &session)
	if session.SignedIn || !session.SetupRequired {
		t.Errorf("session = %+v; want signed out with setup required", session)
	}
	token := e.srv.setupToken
	if token == "" {
		t.Fatal("no setup token while no password is set")
	}

	r := e.sendJSON(browser, http.MethodPost, "/api/setup", map[string]string{"token": token, "password": "too short"})
	e.expect(r, http.StatusBadRequest)
	wantBody(t, r, "at least 10 characters")

	r = e.sendJSON(browser, http.MethodPost, "/api/setup", map[string]string{"token": "guessed", "password": "a good long password"})
	e.expect(r, http.StatusForbidden)
	r = e.sendJSON(browser, http.MethodPost, "/api/setup", map[string]string{"password": "a good long password"})
	e.expect(r, http.StatusForbidden)

	r = e.expect(e.sendJSON(browser, http.MethodPost, "/api/setup", map[string]string{"token": token, "password": "a good long password"}), http.StatusNoContent)
	checkSessionCookie(t, r, false)
	e.expect(e.send(browser, http.MethodGet, "/api/state", nil), http.StatusOK)
	e.expect(e.send(browser, http.MethodGet, "/api/session", nil), http.StatusOK).decode(t, &session)
	if !session.SignedIn || session.SetupRequired {
		t.Errorf("after setup, session = %+v; want signed in, no setup", session)
	}

	// The link works once. Nobody can replace the password with it afterwards.
	r = e.sendJSON(newBrowser(), http.MethodPost, "/api/setup", map[string]string{"token": token, "password": "the attacker's password"})
	e.expect(r, http.StatusConflict)
	wantBody(t, r, "already set")
	e.srv.st.mu.Lock()
	leftover := e.srv.setupToken
	e.srv.st.mu.Unlock()
	if leftover != "" {
		t.Error("the setup token is still set after use")
	}
	// The password chosen is the one that works.
	e.expect(e.sendJSON(newBrowser(), http.MethodPost, "/api/login", map[string]string{"password": "a good long password"}), http.StatusNoContent)
}

func TestLoginAndLogout(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	browser := newBrowser()

	r := e.expect(e.sendJSON(browser, http.MethodPost, "/api/login", map[string]string{"password": "wrong password"}), http.StatusUnauthorized)
	wantBody(t, r, "isn't right")
	if len(r.header.Values("Set-Cookie")) != 0 {
		t.Error("a wrong password got a cookie")
	}
	e.expect(e.send(browser, http.MethodGet, "/api/state", nil), http.StatusUnauthorized)

	r = e.expect(e.sendJSON(browser, http.MethodPost, "/api/login", map[string]string{"password": ownerPassword}), http.StatusNoContent)
	checkSessionCookie(t, r, false)
	e.expect(e.send(browser, http.MethodGet, "/api/state", nil), http.StatusOK)

	u, _ := url.Parse(e.url)
	stolen := browser.Jar.Cookies(u)

	r = e.expect(e.send(browser, http.MethodPost, "/api/logout", nil), http.StatusNoContent)
	if c := findCookie(r, sessionCookie); c == nil || c.MaxAge >= 0 {
		t.Errorf("logout doesn't clear the cookie: %+v", c)
	}
	e.expect(e.send(browser, http.MethodGet, "/api/state", nil), http.StatusUnauthorized)

	// A copy of the cookie taken before signing out doesn't work either.
	other := newBrowser()
	other.Jar.SetCookies(u, stolen)
	e.expect(e.send(other, http.MethodGet, "/api/state", nil), http.StatusUnauthorized)
}

func TestSessionsExpire(t *testing.T) {
	t.Parallel()
	s := &sessions{m: map[string]time.Time{}}
	token, expires := s.create()
	if !s.valid(token) {
		t.Fatal("a new session isn't valid")
	}
	if d := time.Until(expires); d < sessionLifetime-time.Minute || d > sessionLifetime {
		t.Errorf("session lasts %v; want %v", d, sessionLifetime)
	}
	if _, stored := s.m[token]; stored {
		t.Error("the cookie value is stored as is; only its hash should be")
	}
	if s.valid("") || s.valid("made-up") {
		t.Error("an unknown session is valid")
	}

	old, _ := s.create()
	s.m[sessionKey(old)] = time.Now().Add(-time.Second)
	if s.valid(old) {
		t.Error("an expired session is valid")
	}
	s.prune()
	if _, ok := s.m[sessionKey(old)]; ok {
		t.Error("prune kept an expired session")
	}
	if !s.valid(token) {
		t.Error("prune removed a session that is still valid")
	}
	s.remove(token)
	if s.valid(token) {
		t.Error("a removed session is valid")
	}
}

func TestSessionCookieIsSecureBehindHTTPS(t *testing.T) {
	t.Parallel()
	proxy := []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	tests := []struct {
		name   string
		cfg    config
		header []string
		secure bool
	}{
		{"plain HTTP", config{}, nil, false},
		{"https public URL", config{PublicURL: "https://files.example.com"}, nil, true},
		{"untrusted X-Forwarded-Proto", config{}, []string{"X-Forwarded-Proto", "https"}, false},
		{"trusted proxy says https", config{TrustedProxies: proxy}, []string{"X-Forwarded-Proto", "https"}, true},
		{"trusted proxy says http", config{TrustedProxies: proxy}, []string{"X-Forwarded-Proto", "http"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := newEnvWith(t, tt.cfg)
			r := e.expect(e.sendJSON(newTerminal(), http.MethodPost, "/api/login", map[string]string{"password": ownerPassword}, tt.header...), http.StatusNoContent)
			checkSessionCookie(t, r, tt.secure)
		})
	}
}

func checkSessionCookie(t *testing.T, r reply, secure bool) {
	t.Helper()
	c := findCookie(r, sessionCookie)
	switch {
	case c == nil:
		t.Error("no session cookie")
	case !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure != secure:
		t.Errorf("session cookie: HttpOnly %v, SameSite %v, Path %q, Secure %v; want HttpOnly, Strict, /, Secure %v",
			c.HttpOnly, c.SameSite, c.Path, c.Secure, secure)
	case time.Until(c.Expires) < sessionLifetime-time.Hour:
		t.Errorf("session cookie expires %v", c.Expires)
	}
}

func findCookie(r reply, name string) *http.Cookie {
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func TestOwnerEndpointsNeedTheOwner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	routes := []string{
		"GET /api/state",
		"POST /api/folders",
		"POST /api/items/delete",
		"GET /api/files/x/content",
		"POST /api/uploads",
		"GET /api/uploads/x",
		"PUT /api/uploads/x",
		"DELETE /api/uploads/x",
		"POST /api/links",
		"PATCH /api/links/x",
		"POST /api/shares",
		"PATCH /api/shares/x",
	}
	u, _ := url.Parse(e.url)
	forged := newBrowser()
	forged.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: "forged", Path: "/"}})
	for _, route := range routes {
		method, path, _ := strings.Cut(route, " ")
		for _, c := range []*http.Client{newTerminal(), forged} {
			r := e.send(c, method, path, strings.NewReader("{}"), "X-Stash", "1", "Content-Type", "application/json")
			if r.status != http.StatusUnauthorized {
				t.Errorf("%s without signing in: status %d; want 401", route, r.status)
			}
		}
		// Changes also need the X-Stash header, which another site can't add to a request.
		if method != http.MethodGet {
			r := e.send(e.owner, method, path, strings.NewReader("{}"), "Content-Type", "application/json")
			if r.status != http.StatusForbidden {
				t.Errorf("%s without X-Stash: status %d; want 403", route, r.status)
			}
		}
	}
	e.expect(e.send(e.owner, http.MethodGet, "/api/state", nil), http.StatusOK)
}

/* ---------- Slowing down password guessing ---------- */

func TestLimiterBacksOff(t *testing.T) {
	t.Parallel()
	l := &limiter{fails: map[string]*failures{}}
	for i := 0; i < 4; i++ {
		l.fail("k")
		if w := l.wait("k"); w > 0 {
			t.Fatalf("waiting %v after %d failures; the first 4 are free", w, i+1)
		}
	}
	l.fail("k")
	if w := l.wait("k"); w <= 0 || w > time.Second {
		t.Errorf("after 5 failures, wait %v; want up to 1s", w)
	}
	l.fail("k")
	if w := l.wait("k"); w <= time.Second || w > 2*time.Second {
		t.Errorf("after 6 failures, wait %v; want up to 2s", w)
	}
	if w := l.wait("other"); w != 0 {
		t.Errorf("another key waits %v", w)
	}
	for i := 0; i < 40; i++ {
		l.fail("k")
	}
	if w := l.wait("k"); w > 15*time.Minute || w < 14*time.Minute {
		t.Errorf("after many failures, wait %v; want 15 minutes at most", w)
	}
	l.succeed("k")
	if w := l.wait("k"); w != 0 {
		t.Errorf("after the right password, wait %v", w)
	}
}

func TestLimiterForgetsOldFailures(t *testing.T) {
	t.Parallel()
	l := &limiter{fails: map[string]*failures{
		"old":     {count: 9, last: time.Now().Add(-2 * time.Hour), until: time.Now().Add(-time.Minute)},
		"blocked": {count: 20, last: time.Now().Add(-2 * time.Hour), until: time.Now().Add(time.Minute)},
		"recent":  {count: 2, last: time.Now()},
	}}
	l.prune()
	if _, ok := l.fails["old"]; ok {
		t.Error("prune kept failures from hours ago")
	}
	if _, ok := l.fails["blocked"]; !ok {
		t.Error("prune lifted a block that hasn't ended")
	}
	if _, ok := l.fails["recent"]; !ok {
		t.Error("prune forgot recent failures")
	}
}

func TestLoginSlowsDownGuessing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		e.expect(e.sendJSON(newTerminal(), http.MethodPost, "/api/login", map[string]string{"password": "guess"}), http.StatusUnauthorized)
	}
	r := e.expect(e.sendJSON(newTerminal(), http.MethodPost, "/api/login", map[string]string{"password": ownerPassword}), http.StatusTooManyRequests)
	wantBody(t, r, "Too many attempts")
}

func TestVisitor(t *testing.T) {
	t.Parallel()
	srv := &server{cfg: config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}}
	tests := []struct {
		remote string
		xff    string
		want   string
	}{
		{"198.51.100.7:1", "", "198.51.100.7"},
		{"[2001:db8:1:2::5]:1", "", "2001:db8:1:2::/64"},
		{"[2001:db8:1:2:ffff:ffff:ffff:ffff]:1", "", "2001:db8:1:2::/64"},
		{"[2001:db8:1:3::5]:1", "", "2001:db8:1:3::/64"},
		{"[::ffff:198.51.100.7]:1", "", "198.51.100.7"},
		{"[fe80::1%eth0]:1", "", "fe80::/64"},
		{"10.0.0.1:1", "2001:db8:9:9:1:2:3:4", "2001:db8:9:9::/64"},
		{"10.0.0.1:1", "203.0.113.9", "203.0.113.9"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tt.remote
		if tt.xff != "" {
			r.Header.Set("X-Forwarded-For", tt.xff)
		}
		if got := srv.visitor(r); got != tt.want {
			t.Errorf("visitor(%s, X-Forwarded-For %q) = %s; want %s", tt.remote, tt.xff, got, tt.want)
		}
	}
}

// An IPv6 connection comes with billions of addresses. Taking a new one doesn't reset the count,
// for signing in, for unlocking a link's page, or for a password on the command line.
func TestGuessingLimitCoversAnIPv6Network(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	l := e.createLink(nil)
	e.changeLink(l.ID, func(l *Link) { l.PasswordHash = fastHash(ownerPassword) })
	guessers := map[string]func(password, from string) reply{
		"signing in": func(password, from string) reply {
			return e.sendJSON(newTerminal(), http.MethodPost, "/api/login", map[string]string{"password": password}, "X-Forwarded-For", from)
		},
		"unlocking a link": func(password, from string) reply {
			return e.sendJSON(newTerminal(), http.MethodPost, "/api/public/d/"+l.Token+"/unlock", map[string]string{"password": password}, "X-Forwarded-For", from)
		},
		"Link-Password": func(password, from string) reply {
			return e.put(l.Token, "a.txt", strings.NewReader("a"), "Link-Password", password, "X-Forwarded-For", from)
		},
	}
	// Each guesser gets its own networks: the page and the command line share one count per link.
	n := 0
	for name, guess := range guessers {
		n++
		for i := 1; i <= 5; i++ {
			if r := guess("guess", fmt.Sprintf("2001:db8:%d:2::%x", n, i)); r.status != http.StatusUnauthorized {
				t.Fatalf("%s: guess %d got %d", name, i, r.status)
			}
		}
		if r := guess(ownerPassword, fmt.Sprintf("2001:db8:%d:2:aaaa:bbbb:cccc:dddd", n)); r.status != http.StatusTooManyRequests {
			t.Errorf("%s: a new address in the same /64 got %d; want 429", name, r.status)
		}
		if r := guess(ownerPassword, fmt.Sprintf("2001:db8:%d:3::1", n)); r.status == http.StatusTooManyRequests || r.status == http.StatusUnauthorized {
			t.Errorf("%s: another /64 got %d; it shouldn't be held up", name, r.status)
		}
	}
}

// Guessing on the link's page and on the command line counts towards the same limit, so switching
// between them gains nothing.
func TestLinkPageAndCommandLineShareTheLimit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(nil)
	e.changeLink(l.ID, func(l *Link) { l.PasswordHash = fastHash("open sesame") })
	for i := 0; i < 3; i++ {
		e.expect(e.put(l.Token, "a.txt", strings.NewReader("a"), "Link-Password", "guess"), http.StatusUnauthorized)
	}
	for i := 0; i < 2; i++ {
		e.expect(e.sendJSON(newBrowser(), http.MethodPost, "/api/public/d/"+l.Token+"/unlock", map[string]string{"password": "guess"}), http.StatusUnauthorized)
	}
	e.expect(e.put(l.Token, "a.txt", strings.NewReader("a"), "Link-Password", "open sesame"), http.StatusTooManyRequests)
	e.expect(e.sendJSON(newBrowser(), http.MethodPost, "/api/public/d/"+l.Token+"/unlock", map[string]string{"password": "open sesame"}), http.StatusTooManyRequests)
}

// Without -trusted-proxies, X-Forwarded-For is whatever the client wants it to be. Believing it
// would give every guess a fresh address and no limit at all.
func TestGuessingLimitIgnoresForgedForwardedFor(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		e.expect(e.sendJSON(newTerminal(), http.MethodPost, "/api/login", map[string]string{"password": "guess"},
			"X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i)), http.StatusUnauthorized)
	}
	e.expect(e.sendJSON(newTerminal(), http.MethodPost, "/api/login", map[string]string{"password": "guess"},
		"X-Forwarded-For", "198.51.100.99"), http.StatusTooManyRequests)
}

// Behind a trusted proxy each visitor has their own limit, so one person's guesses don't lock out
// the owner.
func TestGuessingLimitIsPerVisitorBehindATrustedProxy(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	for i := 0; i < 5; i++ {
		e.expect(e.sendJSON(newTerminal(), http.MethodPost, "/api/login", map[string]string{"password": "guess"},
			"X-Forwarded-For", "203.0.113.5"), http.StatusUnauthorized)
	}
	e.expect(e.sendJSON(newTerminal(), http.MethodPost, "/api/login", map[string]string{"password": ownerPassword},
		"X-Forwarded-For", "203.0.113.5"), http.StatusTooManyRequests)
	e.expect(e.sendJSON(newTerminal(), http.MethodPost, "/api/login", map[string]string{"password": ownerPassword},
		"X-Forwarded-For", "198.51.100.7"), http.StatusNoContent)
}

/* ---------- Unlocked password-protected links ---------- */

func TestUnlockCookieCantBeForged(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	hash := fastHash("link password")
	request := func(name, value string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: name, Value: value})
		return r
	}
	future := time.Now().Add(time.Hour).Unix()
	good := e.srv.unlockValue("link", "L1", hash, future)
	if !e.srv.isUnlocked(request(unlockCookieName("link", "L1"), good), "link", "L1", hash) {
		t.Fatal("a valid unlock cookie doesn't work")
	}
	if !e.srv.isUnlocked(httptest.NewRequest(http.MethodGet, "/", nil), "link", "L1", "") {
		t.Error("a link without a password is locked")
	}

	_, sig, _ := strings.Cut(good, ".")
	stretched := fmt.Sprintf("%d.%s", time.Now().Add(365*24*time.Hour).Unix(), sig)
	past := time.Now().Add(-time.Second).Unix()
	cases := map[string]*http.Request{
		"no cookie":              httptest.NewRequest(http.MethodGet, "/", nil),
		"empty":                  request(unlockCookieName("link", "L1"), ""),
		"garbage":                request(unlockCookieName("link", "L1"), "garbage"),
		"stretched expiry":       request(unlockCookieName("link", "L1"), stretched),
		"expired":                request(unlockCookieName("link", "L1"), e.srv.unlockValue("link", "L1", hash, past)),
		"other link's cookie":    request(unlockCookieName("link", "L1"), e.srv.unlockValue("link", "L2", hash, future)),
		"share with the same ID": request(unlockCookieName("link", "L1"), e.srv.unlockValue("share", "L1", hash, future)),
		"old password":           request(unlockCookieName("link", "L1"), e.srv.unlockValue("link", "L1", fastHash("old"), future)),
	}
	for name, r := range cases {
		if e.srv.isUnlocked(r, "link", "L1", hash) {
			t.Errorf("%s: the link is unlocked", name)
		}
	}

	// Another Stash has another secret, so its cookies are worthless here.
	other := newEnv(t)
	if e.srv.isUnlocked(request(unlockCookieName("link", "L1"), other.srv.unlockValue("link", "L1", hash, future)), "link", "L1", hash) {
		t.Error("a cookie signed by another Stash unlocks the link")
	}
}

/* ---------- Visitor address behind a reverse proxy ---------- */

func TestClientIP(t *testing.T) {
	t.Parallel()
	srv := &server{cfg: config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}}}
	tests := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"direct", "198.51.100.7:1234", nil, "198.51.100.7"},
		{"untrusted sender's header is ignored", "198.51.100.7:1234", []string{"203.0.113.9"}, "198.51.100.7"},
		{"trusted proxy", "10.0.0.1:1234", []string{"203.0.113.9"}, "203.0.113.9"},
		{"chain of trusted proxies", "10.0.0.1:1234", []string{"203.0.113.9, 10.0.0.2"}, "203.0.113.9"},
		{"client can't prepend an address", "10.0.0.1:1234", []string{"1.1.1.1, 203.0.113.9"}, "203.0.113.9"},
		{"several headers", "10.0.0.1:1234", []string{"1.1.1.1", "203.0.113.9"}, "203.0.113.9"},
		{"garbage stops the walk", "10.0.0.1:1234", []string{"203.0.113.9, nonsense"}, "10.0.0.1"},
		{"only proxies", "10.0.0.1:1234", []string{"10.0.0.3"}, "10.0.0.3"},
		{"proxy without header", "10.0.0.1:1234", nil, "10.0.0.1"},
		{"IPv4 in IPv6", "[::ffff:10.0.0.1]:1234", []string{"203.0.113.9"}, "203.0.113.9"},
		{"IPv6 proxy", "[fd00::1]:1234", []string{"2001:db8::5"}, "2001:db8::5"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tt.remote
		for _, v := range tt.xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		if got := srv.clientIP(r).String(); got != tt.want {
			t.Errorf("%s: clientIP = %s; want %s", tt.name, got, tt.want)
		}
	}
}

func TestOriginAndHTTPS(t *testing.T) {
	t.Parallel()
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	tests := []struct {
		name      string
		cfg       config
		remote    string
		tls       bool
		header    []string
		wantHTTPS bool
		want      string
	}{
		{"plain", config{}, "198.51.100.7:1", false, nil, false, "http://stash.test"},
		{"TLS", config{}, "198.51.100.7:1", true, nil, true, "https://stash.test"},
		{"public URL wins", config{PublicURL: "https://files.example.com"}, "198.51.100.7:1", false,
			[]string{"X-Forwarded-Host", "evil.example"}, true, "https://files.example.com"},
		{"untrusted forwarded headers", config{}, "198.51.100.7:1", false,
			[]string{"X-Forwarded-Proto", "https", "X-Forwarded-Host", "evil.example"}, false, "http://stash.test"},
		{"trusted forwarded headers", config{TrustedProxies: trusted}, "10.0.0.1:1", false,
			[]string{"X-Forwarded-Proto", "HTTPS", "X-Forwarded-Host", "files.example.com"}, true, "https://files.example.com"},
	}
	for _, tt := range tests {
		srv := &server{cfg: tt.cfg}
		r := httptest.NewRequest(http.MethodGet, "http://stash.test/", nil)
		r.RemoteAddr = tt.remote
		if tt.tls {
			r.TLS = &tls.ConnectionState{}
		}
		for i := 0; i+1 < len(tt.header); i += 2 {
			r.Header.Set(tt.header[i], tt.header[i+1])
		}
		if got := srv.isHTTPS(r); got != tt.wantHTTPS {
			t.Errorf("%s: isHTTPS = %v; want %v", tt.name, got, tt.wantHTTPS)
		}
		if got := srv.origin(r); got != tt.want {
			t.Errorf("%s: origin = %s; want %s", tt.name, got, tt.want)
		}
	}
}

// Stash warns once about a reverse proxy it doesn't trust, so the owner can fix the setting.
// Not parallel: it captures the log.
func TestUntrustedProxyWarning(t *testing.T) {
	var buf lockedBuffer
	log.SetOutput(&buf)
	defer log.SetOutput(io.Discard)

	e := newEnv(t)
	for i := 0; i < 3; i++ {
		e.expect(e.get("/api/session", "X-Forwarded-For", "203.0.113.9"), http.StatusOK)
	}
	if n := strings.Count(buf.String(), "isn't in -trusted-proxies"); n != 1 {
		t.Errorf("warned %d times; want once. Log:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "-trusted-proxies 127.0.0.1") {
		t.Errorf("the warning doesn't say which address to add. Log:\n%s", buf.String())
	}

	buf.Reset()
	trusted := newEnvWith(t, config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
	trusted.expect(trusted.get("/api/session", "X-Forwarded-For", "203.0.113.9"), http.StatusOK)
	if strings.Contains(buf.String(), "trusted-proxies") {
		t.Errorf("warned about a trusted proxy. Log:\n%s", buf.String())
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

/* ---------- Settings ---------- */

// Not parallel: it sets environment variables.
func TestLoadConfig(t *testing.T) {
	cfg, err := loadConfig("stash", []string{
		"-listen", "127.0.0.1:9000", "-data", "/srv/stash", "-public-url", "https://files.example.com/",
		"-trusted-proxies", "10.0.0.1, 192.168.1.7/16 ,::1,,",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:9000" || cfg.DataDir != "/srv/stash" {
		t.Errorf("listen %q, data %q", cfg.Listen, cfg.DataDir)
	}
	if cfg.PublicURL != "https://files.example.com" {
		t.Errorf("public URL %q; want it without the trailing slash", cfg.PublicURL)
	}
	want := []string{"10.0.0.1/32", "192.168.0.0/16", "::1/128"}
	if len(cfg.TrustedProxies) != len(want) {
		t.Fatalf("trusted proxies %v; want %v", cfg.TrustedProxies, want)
	}
	for i, p := range cfg.TrustedProxies {
		if p.String() != want[i] {
			t.Errorf("trusted proxy %d = %s; want %s", i, p, want[i])
		}
	}

	for _, bad := range []string{"nonsense", "10.0.0.1/99", "300.1.1.1"} {
		if _, err := loadConfig("stash", []string{"-trusted-proxies", bad}); err == nil {
			t.Errorf("-trusted-proxies %q is accepted", bad)
		}
	}
	if _, err := loadConfig("stash", []string{"-no-such-flag"}); err == nil {
		t.Error("an unknown flag is accepted")
	}

	t.Setenv("STASH_LISTEN", ":7000")
	t.Setenv("STASH_TRUSTED_PROXIES", "172.16.0.0/12")
	cfg, err = loadConfig("stash", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":7000" || len(cfg.TrustedProxies) != 1 || cfg.TrustedProxies[0].String() != "172.16.0.0/12" {
		t.Errorf("settings from the environment aren't used: %+v", cfg)
	}
	cfg, _ = loadConfig("stash", []string{"-listen", ":7001"})
	if cfg.Listen != ":7001" {
		t.Errorf("a flag doesn't win over the environment: listen %q", cfg.Listen)
	}
}

func TestResetPassword(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.ownerHash = fastHash(ownerPassword)
	st.links = append(st.links, &Link{ID: "l1", Token: "t1", Name: "keep me", Folder: requestsID, Quota: 1})
	err = st.save()
	st.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	if err := resetPassword([]string{"-data", dir}); err != nil {
		t.Fatal(err)
	}
	after, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if after.ownerHash != "" {
		t.Error("the password is still set")
	}
	if len(after.links) != 1 || string(after.secret) != string(st.secret) {
		t.Error("resetting the password changed other data")
	}

	// The next start offers the setup link again.
	e := startServer(t, config{DataDir: dir})
	if e.srv.setupToken == "" {
		t.Error("no setup link after resetting the password")
	}
}
