package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

/* ---------- From the command line ---------- */

func TestCurlUpload(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(nil)

	r := e.expect(e.put(l.Token, "hello.txt", strings.NewReader("hello")), http.StatusOK)
	if r.body != "Uploaded hello.txt, 5 B.\n" {
		t.Errorf("reply = %q", r.body)
	}
	if ct := r.header.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type = %q; terminals want plain text", ct)
	}
	f, ok := e.filesIn(l.Folder)["hello.txt"]
	if !ok || f.Size != 5 || e.blob(f.ID) != "hello" {
		t.Fatalf("stored %+v", f)
	}

	r = e.expect(e.put(l.Token, "hello.txt", strings.NewReader("again")), http.StatusOK)
	if r.body != "Uploaded hello.txt (saved as hello (2).txt, a file with that name already exists), 5 B.\n" {
		t.Errorf("reply = %q", r.body)
	}
	if f := e.filesIn(l.Folder)["hello (2).txt"]; e.blob(f.ID) != "again" {
		t.Error("the second upload overwrote the first")
	}

	e.expect(e.put(l.Token, "Tax return (final) é #1.pdf", strings.NewReader("pdf")), http.StatusOK)
	e.expect(e.put(l.Token, "empty.txt", bytes.NewReader(nil)), http.StatusOK)
	if f := e.filesIn(l.Folder)["empty.txt"]; f.ID == "" || f.Size != 0 {
		t.Errorf("empty file stored as %+v", f)
	}
	if _, ok := e.filesIn(l.Folder)["Tax return (final) é #1.pdf"]; !ok {
		t.Errorf("a name with spaces, brackets and accents was changed: %v", e.filesIn(l.Folder))
	}
	e.checkNothingReserved()
}

// The name in the URL can't place a file anywhere but the link's folder.
func TestCurlUploadNamesStayInTheFolder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(nil)
	for name, want := range map[string]string{
		"../../escape.txt":     "escape.txt",
		`C:\Users\me\scan.jpg`: "scan.jpg",
		"..%2F..%2Fx.txt":      "..%2F..%2Fx.txt", // sent escaped twice, so it arrives as plain text
	} {
		e.expect(e.put(l.Token, name, strings.NewReader("x")), http.StatusOK)
		if _, ok := e.filesIn(l.Folder)[want]; !ok {
			t.Errorf("%q wasn't stored as %q: %v", name, want, e.filesIn(l.Folder))
		}
	}
	if _, err := os.Stat(filepath.Join(e.srv.cfg.DataDir, "..", "escape.txt")); !os.IsNotExist(err) {
		t.Error("a file was written outside the data folder")
	}
	r := e.expect(e.put(l.Token, " ", strings.NewReader("x")), http.StatusBadRequest)
	wantBody(t, r, "The file needs a name.")
	e.checkNothingReserved()
}

func TestCurlUploadRefusals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	revoked := e.createLink(nil)
	e.changeLink(revoked.ID, func(l *Link) { l.Revoked = true })
	expired := e.createLink(nil)
	e.changeLink(expired.ID, expireLink)
	small := e.createLink(map[string]any{"quota": 10})
	full := e.createLink(map[string]any{"quota": 5})
	e.expect(e.put(full.Token, "five.txt", strings.NewReader("12345")), http.StatusOK)
	oneFile := e.createLink(map[string]any{"maxFiles": 1})
	e.expect(e.put(oneFile.Token, "first.txt", strings.NewReader("1")), http.StatusOK)

	tests := []struct {
		name   string
		token  string
		size   int
		status int
		msg    string
	}{
		{"unknown link", "no-such-token", 1, http.StatusNotFound, "This upload link doesn't work."},
		{"revoked link", revoked.Token, 1, http.StatusNotFound, "This upload link doesn't work."},
		{"expired link", expired.Token, 1, http.StatusGone, "This upload link has expired."},
		{"file too large", small.Token, 11, http.StatusRequestEntityTooLarge, "Too large: only 10 B is left on this link."},
		{"full link", full.Token, 1, http.StatusRequestEntityTooLarge, "This upload link is full."},
		{"enough files", oneFile.Token, 1, http.StatusRequestEntityTooLarge, "This upload link is full."},
	}
	for _, tt := range tests {
		r := e.put(tt.token, "file.bin", bytes.NewReader(make([]byte, tt.size)))
		if r.status != tt.status || !strings.Contains(r.body, tt.msg) {
			t.Errorf("%s: %d %q; want %d with %q", tt.name, r.status, r.body, tt.status, tt.msg)
		}
	}
	if n := len(e.filesIn(small.Folder)); n != 0 {
		t.Errorf("%d files stored on the small link", n)
	}
	e.expect(e.put(small.Token, "ten.bin", bytes.NewReader(make([]byte, 10))), http.StatusOK)
	e.checkNothingReserved()
}

func TestCurlUploadWithPassword(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"quota": 10})
	e.changeLink(l.ID, func(l *Link) { l.PasswordHash = fastHash("let me in") })

	r := e.expect(e.put(l.Token, "a.txt", strings.NewReader("a")), http.StatusUnauthorized)
	wantBody(t, r, `-H "Link-Password: YOUR-PASSWORD"`, `-Headers @{ 'Link-Password' = 'YOUR-PASSWORD' }`)
	r = e.expect(e.put(l.Token, "a.txt", strings.NewReader("a"), "Link-Password", "wrong"), http.StatusUnauthorized)
	wantBody(t, r, "That password isn't right.")
	e.expect(e.put(l.Token, "a.txt", strings.NewReader("a"), "Link-Password", "let me in"), http.StatusOK)

	// A browser that entered the password on the page doesn't need the header.
	browser := newBrowser()
	e.expect(e.sendJSON(browser, http.MethodPost, "/api/public/d/"+l.Token+"/unlock", map[string]string{"password": "let me in"}), http.StatusNoContent)
	e.expect(e.send(browser, http.MethodPut, "/d/"+l.Token+"/b.txt", strings.NewReader("b")), http.StatusOK)

	// Nothing about the link shows before the password, not even that it's full.
	e.expect(e.put(l.Token, "c.bin", bytes.NewReader(make([]byte, 8)), "Link-Password", "let me in"), http.StatusOK)
	r = e.expect(e.put(l.Token, "d.txt", strings.NewReader("d")), http.StatusUnauthorized)
	wantNoBody(t, r, "full", "Test link")
	r = e.expect(e.put(l.Token, "d.txt", strings.NewReader("d"), "Link-Password", "let me in"), http.StatusRequestEntityTooLarge)
	wantBody(t, r, "full")

	// Whether it expired may show.
	e.changeLink(l.ID, expireLink)
	e.expect(e.put(l.Token, "e.txt", strings.NewReader("e")), http.StatusGone)
}

func TestCurlUploadPasswordGuessingIsSlowedDown(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(nil)
	e.changeLink(l.ID, func(l *Link) { l.PasswordHash = fastHash("let me in") })
	for i := 0; i < 5; i++ {
		e.expect(e.put(l.Token, "a.txt", strings.NewReader("a"), "Link-Password", "guess"), http.StatusUnauthorized)
	}
	r := e.expect(e.put(l.Token, "a.txt", strings.NewReader("a"), "Link-Password", "let me in"), http.StatusTooManyRequests)
	wantBody(t, r, "Too many wrong passwords")

	// The limit is per link: another link's password still works.
	other := e.createLink(nil)
	e.changeLink(other.ID, func(l *Link) { l.PasswordHash = fastHash("other") })
	e.expect(e.put(other.Token, "a.txt", strings.NewReader("a"), "Link-Password", "other"), http.StatusOK)
}

// PowerShell pipes and curl -T - send no Content-Length. Stash then sets aside what's left on the
// link and settles on the real size at the end.
func TestCurlUploadWithoutLength(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"quota": 10_000})
	r := e.expect(e.put(l.Token, "stream.bin", unknownReader{bytes.NewReader(make([]byte, 3000))}), http.StatusOK)
	if r.body != "Uploaded stream.bin, 3 KB.\n" {
		t.Errorf("reply = %q", r.body)
	}
	if f := e.filesIn(l.Folder)["stream.bin"]; f.Size != 3000 {
		t.Errorf("stored size %d; want 3000", f.Size)
	}
	e.checkNothingReserved()

	small := e.createLink(map[string]any{"quota": 1000})
	r = e.expect(e.put(small.Token, "big.bin", unknownReader{bytes.NewReader(make([]byte, 3000))}), http.StatusRequestEntityTooLarge)
	wantBody(t, r, "Too large: there isn't enough space left for this file.")
	if n := len(e.filesIn(small.Folder)); n != 0 {
		t.Errorf("%d files stored", n)
	}
	e.checkNothingReserved()
	// The space it had set aside is back.
	e.expect(e.put(small.Token, "fits.bin", unknownReader{bytes.NewReader(make([]byte, 1000))}), http.StatusOK)
	e.checkNothingReserved()
}

// PowerShell and wget send the whole file before they read the reply. Stash reads it first, so
// they show its message rather than a broken connection. Go's own client copes with an early
// reply, so this talks HTTP over a plain connection, the way those tools do.
func TestRefusedUploadIsReadBeforeTheReply(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"quota": 10})
	const size = 32 << 20 // more than the connection buffers hold, less than what Stash reads
	conn, err := net.Dial("tcp", strings.TrimPrefix(e.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Minute))
	fmt.Fprintf(conn, "PUT /d/%s/big.bin HTTP/1.1\r\nHost: stash\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", l.Token, size)
	chunk := make([]byte, 1<<20)
	for sent := 0; sent < size; sent += len(chunk) {
		if _, err := conn.Write(chunk); err != nil {
			t.Fatalf("the connection broke after %d bytes: %v", sent, err)
		}
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no reply after sending the file: %v", err)
	}
	text, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(text), "Too large: only 10 B is left on this link.") {
		t.Errorf("got %d %q", resp.StatusCode, text)
	}
	e.checkNothingReserved()
}

// curl asks first (Expect: 100-continue), and gets the refusal without sending the file.
func TestRefusedUploadIsntSentWhenCurlAsksFirst(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"quota": 10})
	body := &countingReader{r: bytes.NewReader(make([]byte, 5<<20))}
	req, err := http.NewRequest(http.MethodPut, e.url+"/d/"+l.Token+"/big.bin", body)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = 5 << 20
	req.Header.Set("Expect", "100-continue")
	client := &http.Client{Transport: &http.Transport{ExpectContinueTimeout: 30 * time.Second}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(text), "only 10 B is left") {
		t.Errorf("got %d %q", resp.StatusCode, text)
	}
	if n := body.count(); n != 0 {
		t.Errorf("curl had to send %d bytes before it was refused", n)
	}
}

type countingReader struct {
	mu sync.Mutex
	r  io.Reader
	n  int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.mu.Lock()
	c.n += n
	c.mu.Unlock()
	return n, err
}

func (c *countingReader) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// Opening an upload link in a terminal explains how to upload. The text is the same for every
// link, real or not, so it gives nothing away.
func TestUploadLinkInATerminal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"name": "Secret project"})
	e.changeLink(l.ID, func(l *Link) { l.PasswordHash = fastHash("pw") })

	r := e.expect(e.get("/d/"+l.Token), http.StatusOK)
	wantBody(t, r,
		"curl -T ./file.zip "+e.url+"/d/"+l.Token+"/",
		`curl.exe -T .\file.zip `+e.url+"/d/"+l.Token+"/",
		"Invoke-RestMethod -Method Put -InFile .\\file.zip -Uri "+e.url+"/d/"+l.Token+"/file.zip",
		`-H "Link-Password: YOUR-PASSWORD"`)
	wantNoBody(t, r, "Secret project")

	unknown := e.expect(e.get("/d/made-up"), http.StatusOK)
	if strings.ReplaceAll(unknown.body, "made-up", l.Token) != r.body {
		t.Errorf("an unknown link gets a different reply:\n%s\nversus\n%s", unknown.body, r.body)
	}

	page := e.expect(e.get("/d/"+l.Token, "Accept", "text/html,application/xhtml+xml"), http.StatusOK)
	if !strings.HasPrefix(page.header.Get("Content-Type"), "text/html") || page.header.Get("Content-Security-Policy") != pageCSP {
		t.Errorf("a browser gets %q with CSP %q", page.header.Get("Content-Type"), page.header.Get("Content-Security-Policy"))
	}
}

/* ---------- From the browser, in chunks ---------- */

type linkInfo struct {
	Status   string
	Name     string
	Message  string
	Quota    int64
	Used     int64
	Files    int
	MaxFiles int
	Expires  int64
	Password bool
	Locked   bool
}

func (e *testEnv) linkInfo(c *http.Client, token string, status int) linkInfo {
	e.t.Helper()
	var info linkInfo
	e.expect(e.send(c, http.MethodGet, "/api/public/d/"+token, nil), status).decode(e.t, &info)
	return info
}

// startPublic starts a browser upload through a link and returns its ID.
func (e *testEnv) startPublic(c *http.Client, token, name string, size int) string {
	e.t.Helper()
	var started struct{ ID string }
	e.expect(e.sendJSON(c, http.MethodPost, "/api/public/d/"+token+"/uploads", map[string]any{"name": name, "size": size}), http.StatusCreated).decode(e.t, &started)
	return started.ID
}

func (e *testEnv) chunk(c *http.Client, token, id string, offset int, data string) reply {
	e.t.Helper()
	return e.send(c, http.MethodPut, "/api/public/d/"+token+"/uploads/"+id+"?offset="+strconv.Itoa(offset), strings.NewReader(data))
}

func TestBrowserUpload(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"name": "Photos please", "quota": 100, "message": "Hi there"})
	b := newBrowser()

	info := e.linkInfo(b, l.Token, http.StatusOK)
	if info.Status != "active" || info.Name != "Photos please" || info.Message != "Hi there" || info.Quota != 100 || info.Used != 0 || info.Files != 0 || info.Password {
		t.Errorf("link info = %+v", info)
	}

	id := e.startPublic(b, l.Token, "photo.jpg", 10)
	if info := e.linkInfo(b, l.Token, http.StatusOK); info.Used != 10 || info.Files != 1 {
		t.Errorf("during the upload, used %d and files %d; want the upload counted", info.Used, info.Files)
	}
	status := e.expect(e.send(b, http.MethodGet, "/api/public/d/"+l.Token+"/uploads/"+id, nil), http.StatusOK).fields(t)
	if status["received"] != 0.0 || status["size"] != 10.0 {
		t.Errorf("status = %v", status)
	}

	r := e.expect(e.chunk(b, l.Token, id, 0, "abcd"), http.StatusOK)
	if got := r.fields(t)["received"]; got != 4.0 {
		t.Errorf("received = %v; want 4", got)
	}
	// A chunk sent twice, say after a dropped connection, is told where to go on.
	r = e.expect(e.chunk(b, l.Token, id, 0, "abcd"), http.StatusConflict)
	if got := r.fields(t)["received"]; got != 4.0 {
		t.Errorf("after a repeated chunk, received = %v; want 4", got)
	}
	e.expect(e.send(b, http.MethodPut, "/api/public/d/"+l.Token+"/uploads/"+id, strings.NewReader("x")), http.StatusBadRequest)
	e.expect(e.chunk(b, l.Token, id, -1, "x"), http.StatusBadRequest)

	r = e.expect(e.chunk(b, l.Token, id, 4, "efghij"), http.StatusOK)
	var done struct {
		Done bool
		Item Item
	}
	r.decode(t, &done)
	if !done.Done || done.Item.Name != "photo.jpg" || done.Item.Size != 10 || done.Item.Parent != l.Folder {
		t.Errorf("finished upload = %+v", done)
	}
	if got := e.blob(done.Item.ID); got != "abcdefghij" {
		t.Errorf("stored %q", got)
	}
	e.expect(e.send(b, http.MethodGet, "/api/public/d/"+l.Token+"/uploads/"+id, nil), http.StatusNotFound)
	if info := e.linkInfo(b, l.Token, http.StatusOK); info.Used != 10 || info.Files != 1 {
		t.Errorf("after the upload, used %d and files %d", info.Used, info.Files)
	}
	e.checkNothingReserved()
}

func TestBrowserUploadCantGrow(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"quota": 100})
	b := newBrowser()
	id := e.startPublic(b, l.Token, "a.txt", 5)
	r := e.expect(e.chunk(b, l.Token, id, 0, "123456"), http.StatusRequestEntityTooLarge)
	wantBody(t, r, "larger than it said")
	e.expect(e.send(b, http.MethodGet, "/api/public/d/"+l.Token+"/uploads/"+id, nil), http.StatusNotFound)
	if n := len(e.filesIn(l.Folder)); n != 0 {
		t.Errorf("%d files stored", n)
	}
	e.checkNothingReserved()
}

func TestBrowserUploadCancel(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"quota": 100})
	b := newBrowser()
	id := e.startPublic(b, l.Token, "a.txt", 50)
	e.expect(e.chunk(b, l.Token, id, 0, "0123456789"), http.StatusOK)
	e.expect(e.send(b, http.MethodDelete, "/api/public/d/"+l.Token+"/uploads/"+id, nil), http.StatusNoContent)
	e.expect(e.send(b, http.MethodGet, "/api/public/d/"+l.Token+"/uploads/"+id, nil), http.StatusNotFound)
	e.expect(e.chunk(b, l.Token, id, 10, "x"), http.StatusNotFound)
	if info := e.linkInfo(b, l.Token, http.StatusOK); info.Used != 0 || info.Files != 0 {
		t.Errorf("after cancelling, used %d and files %d", info.Used, info.Files)
	}
	e.checkNothingReserved()
}

// An upload belongs to the link it started on. Knowing its ID isn't enough to touch it from
// another link, or from the owner's side.
func TestUploadsBelongToTheirLink(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.createLink(nil)
	b := e.createLink(nil)
	browser := newBrowser()
	id := e.startPublic(browser, a.Token, "a.txt", 4)

	e.expect(e.chunk(browser, b.Token, id, 0, "abcd"), http.StatusNotFound)
	e.expect(e.send(browser, http.MethodDelete, "/api/public/d/"+b.Token+"/uploads/"+id, nil), http.StatusNotFound)
	e.expect(e.send(e.owner, http.MethodPut, "/api/uploads/"+id+"?offset=0", strings.NewReader("abcd"), "X-Stash", "1"), http.StatusNotFound)

	var owners struct{ ID string }
	e.expect(e.api(http.MethodPost, "/api/uploads", map[string]any{"parent": rootID, "name": "mine.txt", "size": 4}), http.StatusCreated).decode(t, &owners)
	e.expect(e.chunk(browser, a.Token, owners.ID, 0, "evil"), http.StatusNotFound)

	e.expect(e.chunk(browser, a.Token, id, 0, "abcd"), http.StatusOK)
	e.expect(e.send(e.owner, http.MethodDelete, "/api/uploads/"+owners.ID, nil, "X-Stash", "1"), http.StatusNoContent)
	e.checkNothingReserved()
}

func TestBrowserUploadOnLockedLink(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"name": "Locked", "message": "private message"})
	e.changeLink(l.ID, func(l *Link) { l.PasswordHash = fastHash("open sesame") })
	b := newBrowser()

	r := e.expect(e.send(b, http.MethodGet, "/api/public/d/"+l.Token, nil), http.StatusUnauthorized)
	fields := r.fields(t)
	if len(fields) != 4 || fields["status"] != "active" || fields["locked"] != true || fields["name"] != "Locked" || fields["error"] == nil {
		t.Errorf("a locked link shows %v; want only its status, name and that it's locked", fields)
	}
	wantNoBody(t, r, "private message", "quota")
	e.expect(e.sendJSON(b, http.MethodPost, "/api/public/d/"+l.Token+"/uploads", map[string]any{"name": "a.txt", "size": 1}), http.StatusUnauthorized)

	e.expect(e.sendJSON(b, http.MethodPost, "/api/public/d/"+l.Token+"/unlock", map[string]string{"password": "wrong"}), http.StatusUnauthorized)
	r = e.expect(e.sendJSON(b, http.MethodPost, "/api/public/d/"+l.Token+"/unlock", map[string]string{"password": "open sesame"}), http.StatusNoContent)
	c := findCookie(r, unlockCookieName("link", l.ID))
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || time.Until(c.Expires) < unlockLifetime-time.Hour {
		t.Errorf("unlock cookie = %+v", c)
	}
	if info := e.linkInfo(b, l.Token, http.StatusOK); info.Message != "private message" || !info.Password {
		t.Errorf("after unlocking: %+v", info)
	}
	id := e.startPublic(b, l.Token, "a.txt", 1)
	// Someone else who learns the upload ID still needs the password.
	e.expect(e.chunk(newBrowser(), l.Token, id, 0, "x"), http.StatusNotFound)
	e.expect(e.chunk(b, l.Token, id, 0, "x"), http.StatusOK)

	// Changing the password locks out everyone who unlocked with the old one.
	e.changeLink(l.ID, func(l *Link) { l.PasswordHash = fastHash("new password") })
	e.linkInfo(b, l.Token, http.StatusUnauthorized)
}

func TestLinkUnlockGuessingIsSlowedDown(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(nil)
	e.changeLink(l.ID, func(l *Link) { l.PasswordHash = fastHash("open sesame") })
	for i := 0; i < 5; i++ {
		e.expect(e.sendJSON(newBrowser(), http.MethodPost, "/api/public/d/"+l.Token+"/unlock", map[string]string{"password": "guess"}), http.StatusUnauthorized)
	}
	r := e.expect(e.sendJSON(newBrowser(), http.MethodPost, "/api/public/d/"+l.Token+"/unlock", map[string]string{"password": "open sesame"}), http.StatusTooManyRequests)
	wantBody(t, r, "Too many wrong passwords")

	e.expect(e.sendJSON(newBrowser(), http.MethodPost, "/api/public/d/made-up/unlock", map[string]string{"password": "x"}), http.StatusNotFound)
	e.changeLink(l.ID, func(l *Link) { l.Revoked = true })
	e.expect(e.sendJSON(newBrowser(), http.MethodPost, "/api/public/d/"+l.Token+"/unlock", map[string]string{"password": "x"}), http.StatusNotFound)
}

func TestUploadLinkStatus(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := newBrowser()

	r := e.expect(e.send(b, http.MethodGet, "/api/public/d/made-up", nil), http.StatusNotFound)
	if r.fields(t)["status"] != "gone" {
		t.Errorf("unknown link: %s", r.body)
	}
	revoked := e.createLink(nil)
	e.changeLink(revoked.ID, func(l *Link) { l.Revoked = true })
	if info := e.linkInfo(b, revoked.Token, http.StatusNotFound); info.Status != "gone" || info.Name != "" {
		t.Errorf("revoked link: %+v", info)
	}

	expired := e.createLink(map[string]any{"name": "Old"})
	e.changeLink(expired.ID, func(l *Link) { expireLink(l); l.PasswordHash = fastHash("pw") })
	if info := e.linkInfo(b, expired.Token, http.StatusGone); info.Status != "expired" || info.Expires == 0 || info.Name != "" {
		t.Errorf("expired link: %+v", info)
	}

	full := e.createLink(map[string]any{"quota": 3})
	e.expect(e.put(full.Token, "abc.txt", strings.NewReader("abc")), http.StatusOK)
	if info := e.linkInfo(b, full.Token, http.StatusOK); info.Status != "full" || info.Used != 3 {
		t.Errorf("full link: %+v", info)
	}
	enough := e.createLink(map[string]any{"maxFiles": 1})
	e.expect(e.put(enough.Token, "a.txt", strings.NewReader("a")), http.StatusOK)
	if info := e.linkInfo(b, enough.Token, http.StatusOK); info.Status != "full" || info.Files != 1 || info.MaxFiles != 1 {
		t.Errorf("link with enough files: %+v", info)
	}
	e.expect(e.sendJSON(b, http.MethodPost, "/api/public/d/"+enough.Token+"/uploads", map[string]any{"name": "b.txt", "size": 1}), http.StatusForbidden)
}

// An upload that started may finish when the link expires meanwhile, but not when the owner
// revokes the link.
func TestUploadInProgressWhenTheLinkChanges(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	b := newBrowser()
	expiring := e.createLink(nil)
	id := e.startPublic(b, expiring.Token, "a.txt", 8)
	e.expect(e.chunk(b, expiring.Token, id, 0, "1234"), http.StatusOK)
	e.changeLink(expiring.ID, expireLink)
	e.expect(e.chunk(b, expiring.Token, id, 4, "5678"), http.StatusOK)
	if f := e.filesIn(expiring.Folder)["a.txt"]; f.Size != 8 {
		t.Errorf("the upload didn't finish: %+v", f)
	}

	revoked := e.createLink(nil)
	id = e.startPublic(b, revoked.Token, "b.txt", 8)
	e.expect(e.chunk(b, revoked.Token, id, 0, "1234"), http.StatusOK)
	e.changeLink(revoked.ID, func(l *Link) { l.Revoked = true })
	e.expect(e.chunk(b, revoked.Token, id, 4, "5678"), http.StatusNotFound)
}

/* ---------- Setting space aside ---------- */

// Space is set aside when an upload starts, so uploads running side by side can't add up to more
// than the link allows.
func TestUploadsSetSpaceAside(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"quota": 10})
	b := newBrowser()

	first := e.startPublic(b, l.Token, "a.bin", 6)
	r := e.expect(e.sendJSON(b, http.MethodPost, "/api/public/d/"+l.Token+"/uploads", map[string]any{"name": "b.bin", "size": 6}), http.StatusRequestEntityTooLarge)
	wantBody(t, r, "Too large: only 4 B is left on this link.")
	second := e.startPublic(b, l.Token, "c.bin", 4)
	r = e.expect(e.sendJSON(b, http.MethodPost, "/api/public/d/"+l.Token+"/uploads", map[string]any{"name": "d.bin", "size": 1}), http.StatusRequestEntityTooLarge)
	wantBody(t, r, "This link is full.")
	r = e.expect(e.put(l.Token, "e.bin", strings.NewReader("e")), http.StatusRequestEntityTooLarge)
	wantBody(t, r, "This link is full.")

	e.expect(e.send(b, http.MethodDelete, "/api/public/d/"+l.Token+"/uploads/"+first, nil), http.StatusNoContent)
	third := e.startPublic(b, l.Token, "f.bin", 6)
	e.expect(e.chunk(b, l.Token, second, 0, "1234"), http.StatusOK)
	e.expect(e.chunk(b, l.Token, third, 0, "123456"), http.StatusOK)
	if info := e.linkInfo(b, l.Token, http.StatusOK); info.Status != "full" || info.Used != 10 || info.Files != 2 {
		t.Errorf("link = %+v", info)
	}
	e.checkNothingReserved()

	e.expect(e.sendJSON(b, http.MethodPost, "/api/public/d/"+l.Token+"/uploads", map[string]any{"name": "x", "size": -1}), http.StatusBadRequest)
	e.expect(e.sendJSON(b, http.MethodPost, "/api/public/d/"+l.Token+"/uploads", map[string]any{"name": " / ", "size": 0}), http.StatusBadRequest)
}

func TestUnfinishedUploadsCountTowardsMaxFiles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"maxFiles": 2})
	b := newBrowser()
	first := e.startPublic(b, l.Token, "a.txt", 1)
	e.startPublic(b, l.Token, "b.txt", 1)
	r := e.expect(e.sendJSON(b, http.MethodPost, "/api/public/d/"+l.Token+"/uploads", map[string]any{"name": "c.txt", "size": 1}), http.StatusForbidden)
	wantBody(t, r, "at most 2 files")
	if info := e.linkInfo(b, l.Token, http.StatusOK); info.Status != "full" || info.Files != 2 {
		t.Errorf("link = %+v", info)
	}
	e.expect(e.send(b, http.MethodDelete, "/api/public/d/"+l.Token+"/uploads/"+first, nil), http.StatusNoContent)
	e.startPublic(b, l.Token, "c.txt", 1)
}

func TestConcurrentUploadsStayWithinTheLimit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"quota": 10_000})
	var wg sync.WaitGroup
	statuses := make([]int, 8)
	for i := range statuses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPut, e.url+"/d/"+l.Token+"/part"+strconv.Itoa(i)+".bin", bytes.NewReader(make([]byte, 4000)))
			resp, err := newTerminal().Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			statuses[i] = resp.StatusCode
		}()
	}
	wg.Wait()
	sort.Ints(statuses)
	want := []int{200, 200, 413, 413, 413, 413, 413, 413}
	for i := range want {
		if statuses[i] != want[i] {
			t.Fatalf("statuses %v; want %v", statuses, want)
		}
	}
	var total int64
	for _, f := range e.filesIn(l.Folder) {
		total += f.Size
	}
	if total != 8000 {
		t.Errorf("stored %d bytes; want 8000", total)
	}
	e.checkNothingReserved()
}

func TestUploadsLeaveDiskSpaceFree(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(nil)
	// Pretend other uploads have set aside all the space there is.
	const plenty = 1 << 50
	e.srv.st.mu.Lock()
	e.srv.inflight += plenty
	e.srv.st.mu.Unlock()

	r := e.expect(e.api(http.MethodPost, "/api/uploads", map[string]any{"parent": rootID, "name": "a.txt", "size": 1}), http.StatusRequestEntityTooLarge)
	wantBody(t, r, "Not enough space on the server: 0 B is free.")
	r = e.expect(e.put(l.Token, "a.txt", strings.NewReader("a")), http.StatusRequestEntityTooLarge)
	wantBody(t, r, "Not enough space on the server")
	r = e.expect(e.put(l.Token, "a.txt", unknownReader{strings.NewReader("a")}), http.StatusRequestEntityTooLarge)
	wantBody(t, r, "Not enough space on the server")
	if free := e.state().Storage.Free; free != 0 {
		t.Errorf("state says %d bytes are free", free)
	}

	e.srv.st.mu.Lock()
	e.srv.inflight -= plenty
	e.srv.st.mu.Unlock()
	e.expect(e.put(l.Token, "a.txt", strings.NewReader("a")), http.StatusOK)
}

func TestTooManyUploadsAtOnce(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(nil)
	b := newBrowser()
	var ids []string
	for i := 0; i < maxUploadsPerLink; i++ {
		ids = append(ids, e.startPublic(b, l.Token, "f"+strconv.Itoa(i), 1))
	}
	r := e.expect(e.sendJSON(b, http.MethodPost, "/api/public/d/"+l.Token+"/uploads", map[string]any{"name": "one-more", "size": 1}), http.StatusTooManyRequests)
	wantBody(t, r, "Too many uploads")
	// Another link, and the owner, aren't held up.
	other := e.createLink(nil)
	e.startPublic(b, other.Token, "x", 1)
	e.upload(rootID, "mine.txt", []byte("x"))

	for _, id := range ids {
		e.expect(e.send(b, http.MethodDelete, "/api/public/d/"+l.Token+"/uploads/"+id, nil), http.StatusNoContent)
	}
	e.startPublic(b, l.Token, "now-it-fits", 1)
}

/* ---------- The owner's uploads ---------- */

func TestOwnerUpload(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var folder Item
	e.expect(e.api(http.MethodPost, "/api/folders", map[string]string{"parent": rootID, "name": "Docs"}), http.StatusCreated).decode(t, &folder)

	f := e.upload(folder.ID, "a.txt", []byte("first"))
	if f.Parent != folder.ID || f.Size != 5 {
		t.Errorf("file = %+v", f)
	}
	e.srv.st.mu.Lock()
	modified := e.srv.st.items[folder.ID].Modified
	e.srv.st.mu.Unlock()
	if modified < f.Modified {
		t.Error("the folder's modification time didn't change")
	}
	if again := e.upload(folder.ID, "A.txt", []byte("second")); again.Name != "A (2).txt" {
		t.Errorf("second file is called %q", again.Name)
	}
	if empty := e.upload(rootID, "empty", nil); empty.Size != 0 || e.blob(empty.ID) != "" {
		t.Errorf("empty file = %+v", empty)
	}

	// Several chunks, as the page sends large files.
	var started struct{ ID string }
	e.expect(e.api(http.MethodPost, "/api/uploads", map[string]any{"parent": rootID, "name": "big.bin", "size": 9}), http.StatusCreated).decode(t, &started)
	for i, part := range []string{"abc", "def", "ghi"} {
		e.expect(e.send(e.owner, http.MethodPut, "/api/uploads/"+started.ID+"?offset="+strconv.Itoa(i*3), strings.NewReader(part), "X-Stash", "1"), http.StatusOK)
	}
	if got := e.blob(e.filesIn(rootID)["big.bin"].ID); got != "abcdefghi" {
		t.Errorf("stored %q", got)
	}

	tests := []struct {
		in     map[string]any
		status int
		msg    string
	}{
		{map[string]any{"parent": "nope", "name": "a", "size": 1}, http.StatusNotFound, "doesn't exist anymore"},
		{map[string]any{"parent": f.ID, "name": "a", "size": 1}, http.StatusNotFound, "doesn't exist anymore"},
		{map[string]any{"parent": rootID, "name": "a", "size": -1}, http.StatusBadRequest, "can't be negative"},
		{map[string]any{"parent": rootID, "name": "", "size": 1}, http.StatusBadRequest, "needs a name"},
	}
	for _, tt := range tests {
		r := e.api(http.MethodPost, "/api/uploads", tt.in)
		if r.status != tt.status || !strings.Contains(r.body, tt.msg) {
			t.Errorf("%v: %d %s; want %d with %q", tt.in, r.status, r.body, tt.status, tt.msg)
		}
	}
	e.checkNothingReserved()
}

// Partial uploads can't be resumed after a restart, so Stash clears them out when it starts.
func TestRestartClearsUnfinishedUploads(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var started struct{ ID string }
	e.expect(e.api(http.MethodPost, "/api/uploads", map[string]any{"parent": rootID, "name": "a.bin", "size": 10}), http.StatusCreated).decode(t, &started)
	e.expect(e.send(e.owner, http.MethodPut, "/api/uploads/"+started.ID+"?offset=0", strings.NewReader("123"), "X-Stash", "1"), http.StatusOK)
	uploads := filepath.Join(e.srv.cfg.DataDir, "uploads")
	if parts, _ := os.ReadDir(uploads); len(parts) != 1 {
		t.Fatalf("%d partial files; want 1", len(parts))
	}
	if _, err := newServer(e.srv.cfg); err != nil {
		t.Fatal(err)
	}
	if parts, _ := os.ReadDir(uploads); len(parts) != 0 {
		t.Errorf("%d partial files are left after a restart", len(parts))
	}
}
