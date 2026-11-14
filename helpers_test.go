package main

// Shared helpers for the tests. Each test starts its own Stash with an empty data folder and
// talks to it over real HTTP, the way a browser, curl or PowerShell would.

import (
	"bytes"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// The server logs setup links and hints. Tests check what people get to see instead.
	log.SetOutput(io.Discard)
	os.Exit(m.Run())
}

const ownerPassword = "owner-password-123"

// testEnv is one running Stash, with a browser in which the owner is signed in.
type testEnv struct {
	t     *testing.T
	srv   *server
	url   string
	owner *http.Client
}

// startServer starts a Stash that has no password yet, like on its first start.
func startServer(t *testing.T, cfg config) *testEnv {
	t.Helper()
	if cfg.DataDir == "" {
		cfg.DataDir = t.TempDir()
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	srv, err := newServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.routes())
	t.Cleanup(ts.Close)
	return &testEnv{t: t, srv: srv, url: ts.URL, owner: newBrowser()}
}

// newEnv starts a Stash with the owner password set and the owner signed in.
func newEnv(t *testing.T) *testEnv {
	t.Helper()
	return newEnvWith(t, config{})
}

func newEnvWith(t *testing.T, cfg config) *testEnv {
	t.Helper()
	e := startServer(t, cfg)
	e.srv.st.mu.Lock()
	e.srv.st.ownerHash, e.srv.setupToken = fastHash(ownerPassword), ""
	err := e.srv.st.save()
	e.srv.st.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	token, expires := e.srv.sessions.create()
	u, _ := url.Parse(e.url)
	e.owner.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: token, Path: "/", Expires: expires}})
	return e
}

// in returns the same environment for use in a subtest.
func (e *testEnv) in(t *testing.T) *testEnv {
	c := *e
	c.t = t
	return &c
}

// newBrowser is an HTTP client that keeps cookies and doesn't follow redirects.
func newBrowser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: noRedirects, Timeout: time.Minute}
}

// newTerminal is an HTTP client like curl without a cookie jar.
func newTerminal() *http.Client {
	return &http.Client{CheckRedirect: noRedirects, Timeout: time.Minute}
}

func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// fastHash hashes a password the way hashPassword does, with far fewer rounds so tests stay
// quick. checkPassword reads the number of rounds from the hash, so it accepts these.
func fastHash(password string) string {
	salt := randomBytes(16)
	key, err := pbkdf2.Key(sha256.New, password, salt, 1000, 32)
	if err != nil {
		panic(err)
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$1000$%s$%s", enc.EncodeToString(salt), enc.EncodeToString(key))
}

/* ---------- Requests ---------- */

type reply struct {
	status int
	header http.Header
	body   string
}

// send makes a request and reads the whole reply. header holds name, value pairs.
func (e *testEnv) send(c *http.Client, method, path string, body io.Reader, header ...string) reply {
	e.t.Helper()
	req, err := http.NewRequest(method, e.url+path, body)
	if err != nil {
		e.t.Fatal(err)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("%s %s: reading the reply: %v", method, path, err)
	}
	return reply{resp.StatusCode, resp.Header, string(raw)}
}

// get fetches path as a stranger without cookies, such as curl.
func (e *testEnv) get(path string, header ...string) reply {
	e.t.Helper()
	return e.send(newTerminal(), http.MethodGet, path, nil, header...)
}

// api sends JSON as the signed-in owner's pages do.
func (e *testEnv) api(method, path string, in any) reply {
	e.t.Helper()
	return e.sendJSON(e.owner, method, path, in, "X-Stash", "1")
}

func (e *testEnv) sendJSON(c *http.Client, method, path string, in any, header ...string) reply {
	e.t.Helper()
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			e.t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	return e.send(c, method, path, body, append([]string{"Content-Type", "application/json"}, header...)...)
}

// expect stops the test when the reply doesn't have the wanted status.
func (e *testEnv) expect(r reply, status int) reply {
	e.t.Helper()
	if r.status != status {
		e.t.Fatalf("got status %d, want %d; reply: %s", r.status, status, r.body)
	}
	return r
}

func (r reply) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.body), v); err != nil {
		t.Fatalf("reply isn't the expected JSON: %v; reply: %s", err, r.body)
	}
}

// fields decodes a JSON object reply into a map, to check which fields a reply has.
func (r reply) fields(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	r.decode(t, &m)
	return m
}

func wantBody(t *testing.T, r reply, substrings ...string) {
	t.Helper()
	for _, s := range substrings {
		if !strings.Contains(r.body, s) {
			t.Errorf("reply doesn't contain %q; reply: %s", s, r.body)
		}
	}
}

func wantNoBody(t *testing.T, r reply, substrings ...string) {
	t.Helper()
	for _, s := range substrings {
		if strings.Contains(r.body, s) {
			t.Errorf("reply contains %q, which it shouldn't; reply: %s", s, r.body)
		}
	}
}

/* ---------- Files, links and shares ---------- */

// upload stores a file as the owner, through the same chunked upload the owner's page uses.
func (e *testEnv) upload(parent, name string, content []byte) Item {
	e.t.Helper()
	return e.uploadIn(parent, "", name, content)
}

// uploadIn stores a file as the owner's page does for a dropped folder: path is the folder the
// file was in, such as "Trip/Day 1".
func (e *testEnv) uploadIn(parent, path, name string, content []byte) Item {
	e.t.Helper()
	var started struct{ ID string }
	e.expect(e.api(http.MethodPost, "/api/uploads", map[string]any{"parent": parent, "path": path, "name": name, "size": len(content)}), http.StatusCreated).decode(e.t, &started)
	r := e.expect(e.send(e.owner, http.MethodPut, "/api/uploads/"+started.ID+"?offset=0", bytes.NewReader(content), "X-Stash", "1"), http.StatusOK)
	var done struct {
		Done bool
		Item Item
	}
	r.decode(e.t, &done)
	if !done.Done {
		e.t.Fatalf("upload of %s didn't finish: %s", name, r.body)
	}
	return done.Item
}

// uploadThrough sends a file through an upload link the way its page does, in one chunk. path is
// the folder the file was in when a folder is uploaded, or "".
func (e *testEnv) uploadThrough(c *http.Client, token, path, name, data string) Item {
	e.t.Helper()
	var started struct{ ID string }
	e.expect(e.sendJSON(c, http.MethodPost, "/api/public/d/"+token+"/uploads", map[string]any{"path": path, "name": name, "size": len(data)}), http.StatusCreated).decode(e.t, &started)
	var done struct {
		Done bool
		Item Item
	}
	e.expect(e.send(c, http.MethodPut, "/api/public/d/"+token+"/uploads/"+started.ID+"?offset=0", strings.NewReader(data)), http.StatusOK).decode(e.t, &done)
	if !done.Done {
		e.t.Fatalf("the upload of %s didn't finish", name)
	}
	return done.Item
}

// put uploads a file the way curl -T does, to an upload link.
func (e *testEnv) put(token, name string, content io.Reader, header ...string) reply {
	e.t.Helper()
	return e.send(newTerminal(), http.MethodPut, "/d/"+token+"/"+url.PathEscape(name), content, header...)
}

func (e *testEnv) createLink(fields map[string]any) linkView {
	e.t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	if _, ok := fields["name"]; !ok {
		fields["name"] = "Test link"
	}
	if _, ok := fields["quota"]; !ok {
		fields["quota"] = 1 << 20
	}
	var l linkView
	e.expect(e.api(http.MethodPost, "/api/links", fields), http.StatusCreated).decode(e.t, &l)
	return l
}

func (e *testEnv) createShare(fields map[string]any, files ...Item) shareView {
	e.t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	if _, ok := fields["name"]; !ok {
		fields["name"] = "Test share"
	}
	var ids []string
	for _, f := range files {
		ids = append(ids, f.ID)
	}
	fields["files"] = ids
	var s shareView
	e.expect(e.api(http.MethodPost, "/api/shares", fields), http.StatusCreated).decode(e.t, &s)
	return s
}

// changeLink changes an upload link directly in the store, for things the owner's pages can't
// do, such as letting it expire right now.
func (e *testEnv) changeLink(id string, change func(*Link)) {
	e.t.Helper()
	e.srv.st.mu.Lock()
	defer e.srv.st.mu.Unlock()
	l := e.srv.st.linkByID(id)
	if l == nil {
		e.t.Fatalf("no upload link %s", id)
	}
	change(l)
	if err := e.srv.st.save(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) changeShare(id string, change func(*Share)) {
	e.t.Helper()
	e.srv.st.mu.Lock()
	defer e.srv.st.mu.Unlock()
	s := e.srv.st.shareByID(id)
	if s == nil {
		e.t.Fatalf("no share link %s", id)
	}
	change(s)
	if err := e.srv.st.save(); err != nil {
		e.t.Fatal(err)
	}
}

func expireLink(l *Link)   { l.Expires = nowMillis() - 1000 }
func expireShare(s *Share) { s.Expires = nowMillis() - 1000 }

func (e *testEnv) share(id string) Share {
	e.t.Helper()
	e.srv.st.mu.Lock()
	defer e.srv.st.mu.Unlock()
	s := e.srv.st.shareByID(id)
	if s == nil {
		e.t.Fatalf("no share link %s", id)
	}
	return *s
}

// filesIn lists the files in a folder, by name.
func (e *testEnv) filesIn(folder string) map[string]Item {
	e.srv.st.mu.Lock()
	defer e.srv.st.mu.Unlock()
	out := map[string]Item{}
	for _, it := range e.srv.st.children(folder) {
		if it.Type == "file" {
			out[it.Name] = *it
		}
	}
	return out
}

// pathOf says where an item is below My files, such as "Trip/Day 1/photo.jpg".
func (e *testEnv) pathOf(id string) string {
	e.srv.st.mu.Lock()
	defer e.srv.st.mu.Unlock()
	var names []string
	for it := e.srv.st.items[id]; it != nil && it.ID != rootID; it = e.srv.st.items[it.Parent] {
		names = append([]string{it.Name}, names...)
	}
	return strings.Join(names, "/")
}

// listing names what's in a folder, leaving out the trash, in name order. Folders end in a
// slash: "Day 1/, a.jpg".
func (e *testEnv) listing(folder string) string {
	e.srv.st.mu.Lock()
	defer e.srv.st.mu.Unlock()
	var names []string
	for _, it := range e.srv.st.liveChildren(folder) {
		if it.Type == "folder" {
			names = append(names, it.Name+"/")
		} else {
			names = append(names, it.Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func (e *testEnv) blob(id string) string {
	e.t.Helper()
	raw, err := os.ReadFile(e.srv.st.blobPath(id))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(raw)
}

// checkNothingReserved fails when an upload left space set aside or a partial file behind.
func (e *testEnv) checkNothingReserved() {
	e.t.Helper()
	e.srv.st.mu.Lock()
	uploads, inflight, reserved, pending := len(e.srv.uploads), e.srv.inflight, len(e.srv.reserved), len(e.srv.pending)
	e.srv.st.mu.Unlock()
	if uploads != 0 || inflight != 0 || reserved != 0 || pending != 0 {
		e.t.Errorf("space is still set aside: %d uploads, %d bytes in flight, %d links with reservations, %d with pending files",
			uploads, inflight, reserved, pending)
	}
	parts, err := os.ReadDir(filepath.Join(e.srv.cfg.DataDir, "uploads"))
	if err != nil {
		e.t.Fatal(err)
	}
	for _, p := range parts {
		e.t.Errorf("a partial upload is left behind: %s", p.Name())
	}
}

// unknownReader hides a body's length, so the request goes out without a Content-Length,
// like a PowerShell pipe or curl -T - reading from standard input.
type unknownReader struct{ r io.Reader }

func (u unknownReader) Read(p []byte) (int, error) { return u.r.Read(p) }
