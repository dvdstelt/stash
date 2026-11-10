package main

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func (e *testEnv) fileURL(s shareView, f Item) string { return "/s/" + s.Token + "/files/" + f.ID }

func TestShareDownload(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "notes.txt", []byte("hello there"))
	b := e.upload(rootID, "other.txt", []byte("not shared"))
	s := e.createShare(nil, a)

	r := e.expect(e.get(e.fileURL(s, a)), http.StatusOK)
	if r.body != "hello there" || !strings.HasPrefix(r.header.Get("Content-Disposition"), "inline") {
		t.Errorf("got %q as %q", r.body, r.header.Get("Content-Disposition"))
	}
	r = e.expect(e.get(e.fileURL(s, a)+"?download"), http.StatusOK)
	if r.header.Get("Content-Disposition") != "attachment; filename=notes.txt" || r.header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("with ?download: %q, %q", r.header.Get("Content-Disposition"), r.header.Get("Content-Type"))
	}
	r = e.expect(e.get(e.fileURL(s, b)), http.StatusNotFound)
	wantBody(t, r, "isn't part of this share")
	e.expect(e.get("/s/"+s.Token+"/files/nope"), http.StatusNotFound)
	if got := e.share(s.ID).Downloads; got != 2 {
		t.Errorf("downloads = %d; want 2", got)
	}
}

func TestShareLinkThatDoesntWork(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("a"))
	revoked := e.createShare(nil, a)
	e.changeShare(revoked.ID, func(s *Share) { s.Revoked = true })
	expired := e.createShare(nil, a)
	e.changeShare(expired.ID, expireShare)

	for _, path := range []string{"/s/made-up/files/" + a.ID, "/s/made-up/all.zip", "/s/made-up", e.fileURL(revoked, a), "/s/" + revoked.Token + "/all.zip"} {
		r := e.expect(e.get(path), http.StatusNotFound)
		wantBody(t, r, "This share link doesn't work.")
	}
	for _, path := range []string{e.fileURL(expired, a), "/s/" + expired.Token + "/all.zip", "/s/" + expired.Token} {
		r := e.expect(e.get(path), http.StatusGone)
		wantBody(t, r, "This share link has expired.")
	}
	if got := e.share(expired.ID).Downloads; got != 0 {
		t.Errorf("an expired share counted %d downloads", got)
	}
}

// Opening a share link in a terminal lists curl commands for the files.
func TestShareLinkInATerminal(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "it's here.txt", []byte("12345"))
	b := e.upload(rootID, "$(touch x).bin", bytes.Repeat([]byte("x"), 2500))
	s := e.createShare(map[string]any{"name": "Holiday photos!"}, a, b)
	base := e.url + "/s/" + s.Token

	r := e.expect(e.get("/s/"+s.Token), http.StatusOK)
	wantBody(t, r,
		"Holiday photos!\n",
		"curl -L -o holiday-photos.zip "+base+"/all.zip",
		"it's here.txt (5 B)",
		`curl -L -o 'it'\''s here.txt' '`+base+"/files/"+a.ID+"?download'",
		"$(touch x).bin (2.5 KB)",
		`curl -L -o '$(touch x).bin' '`+base+"/files/"+b.ID+"?download'")
	if got := e.share(s.ID).Downloads; got != 0 {
		t.Errorf("reading the instructions counted %d downloads", got)
	}

	page := e.expect(e.get("/s/"+s.Token, "Accept", "text/html"), http.StatusOK)
	if page.header.Get("Content-Security-Policy") != pageCSP {
		t.Errorf("the page has CSP %q", page.header.Get("Content-Security-Policy"))
	}
	wantNoBody(t, page, "it's here.txt")
}

type shareInfo struct {
	Status   string
	Name     string
	Message  string
	Expires  int64
	Password bool
	Locked   bool
	Limited  bool
	Files    []struct {
		ID, Name  string
		Size      int64
		NoPreview bool
	}
}

func TestShareInfoForThePage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("aaa"))
	mine := e.upload(rootID, "mine.pdf", []byte("%PDF"))
	l := e.createLink(nil)
	e.expect(e.put(l.Token, "theirs.pdf", strings.NewReader("%PDF")), http.StatusOK)
	theirs := e.filesIn(l.Folder)["theirs.pdf"]
	s := e.createShare(map[string]any{"name": "Docs", "message": "Here you go", "expiresDays": 3}, a, mine, theirs)

	var info shareInfo
	e.expect(e.get("/api/public/s/"+s.Token), http.StatusOK).decode(t, &info)
	if info.Status != "active" || info.Name != "Docs" || info.Message != "Here you go" || info.Expires == 0 || info.Password || info.Limited {
		t.Errorf("share info = %+v", info)
	}
	if len(info.Files) != 3 || info.Files[0].Name != "a.txt" || info.Files[0].Size != 3 {
		t.Fatalf("files = %+v", info.Files)
	}
	// A PDF a stranger uploaded always downloads; the owner's own PDF may open.
	if info.Files[0].NoPreview || info.Files[1].NoPreview || !info.Files[2].NoPreview {
		t.Errorf("noPreview = %v, %v, %v; want only the uploaded PDF", info.Files[0].NoPreview, info.Files[1].NoPreview, info.Files[2].NoPreview)
	}

	// With a download limit every view counts, so the page shows no previews.
	limited := e.createShare(map[string]any{"maxDownloads": 2}, a)
	e.expect(e.get("/api/public/s/"+limited.Token), http.StatusOK).decode(t, &info)
	if !info.Limited {
		t.Error("a share with a download limit isn't marked limited")
	}
	if got := e.share(limited.ID).Downloads; got != 0 {
		t.Errorf("looking at the page counted %d downloads", got)
	}
}

func TestShareInfoWhenItCantBeUsed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("a"))

	r := e.expect(e.get("/api/public/s/made-up"), http.StatusNotFound)
	if f := r.fields(t); len(f) != 1 || f["status"] != "gone" {
		t.Errorf("unknown share: %s", r.body)
	}
	revoked := e.createShare(map[string]any{"name": "Revoked"}, a)
	e.changeShare(revoked.ID, func(s *Share) { s.Revoked = true })
	r = e.expect(e.get("/api/public/s/"+revoked.Token), http.StatusNotFound)
	wantNoBody(t, r, "Revoked", "a.txt")

	expired := e.createShare(map[string]any{"name": "Old", "message": "secret note"}, a)
	e.changeShare(expired.ID, expireShare)
	r = e.expect(e.get("/api/public/s/"+expired.Token), http.StatusOK)
	if f := r.fields(t); f["status"] != "expired" || f["expires"] == nil {
		t.Errorf("expired share: %s", r.body)
	}
	wantNoBody(t, r, "Old", "secret note", "a.txt")

	full := e.createShare(map[string]any{"maxDownloads": 1}, a)
	e.expect(e.get(e.fileURL(full, a)), http.StatusOK)
	r = e.expect(e.get("/api/public/s/"+full.Token), http.StatusOK)
	if f := r.fields(t); f["status"] != "full" {
		t.Errorf("full share: %s", r.body)
	}
	wantNoBody(t, r, "a.txt")
}

// A password-protected share shows only its name until the password is in, on the page and on
// the command line.
func TestPasswordProtectedShare(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "secret plans.txt", []byte("the plans"))
	s := e.createShare(map[string]any{"name": "Plans", "message": "for your eyes only"}, a)
	e.changeShare(s.ID, func(s *Share) { s.PasswordHash = fastHash("swordfish") })

	r := e.expect(e.get("/api/public/s/"+s.Token), http.StatusOK)
	if f := r.fields(t); len(f) != 3 || f["status"] != "active" || f["locked"] != true || f["name"] != "Plans" {
		t.Errorf("locked share shows %v; want only its status, name and that it's locked", f)
	}
	for _, path := range []string{"/s/" + s.Token, e.fileURL(s, a), "/s/" + s.Token + "/all.zip"} {
		r := e.expect(e.get(path), http.StatusUnauthorized)
		wantBody(t, r, `-H "Link-Password: YOUR-PASSWORD"`)
		wantNoBody(t, r, "secret plans", "the plans", "Plans", "for your eyes only")
	}
	e.expect(e.get(e.fileURL(s, a), "Link-Password", "wrong"), http.StatusUnauthorized)
	r = e.expect(e.get(e.fileURL(s, a), "Link-Password", "swordfish"), http.StatusOK)
	if r.body != "the plans" {
		t.Errorf("got %q", r.body)
	}
	e.expect(e.get("/s/"+s.Token, "Link-Password", "swordfish"), http.StatusOK)
	if got := e.share(s.ID).Downloads; got != 1 {
		t.Errorf("downloads = %d; wrong passwords shouldn't count", got)
	}

	b := newBrowser()
	e.expect(e.sendJSON(b, http.MethodPost, "/api/public/s/"+s.Token+"/unlock", map[string]string{"password": "wrong"}), http.StatusUnauthorized)
	r = e.expect(e.sendJSON(b, http.MethodPost, "/api/public/s/"+s.Token+"/unlock", map[string]string{"password": "swordfish"}), http.StatusNoContent)
	if c := findCookie(r, unlockCookieName("share", s.ID)); c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("unlock cookie = %+v", c)
	}
	var info shareInfo
	e.expect(e.send(b, http.MethodGet, "/api/public/s/"+s.Token, nil), http.StatusOK).decode(t, &info)
	if info.Locked || !info.Password || info.Message != "for your eyes only" || len(info.Files) != 1 {
		t.Errorf("after unlocking: %+v", info)
	}
	if r := e.expect(e.send(b, http.MethodGet, e.fileURL(s, a), nil), http.StatusOK); r.body != "the plans" {
		t.Errorf("got %q", r.body)
	}
}

func TestShareUnlockGuessingIsSlowedDown(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("a"))
	s := e.createShare(nil, a)
	e.changeShare(s.ID, func(s *Share) { s.PasswordHash = fastHash("swordfish") })
	for i := 0; i < 5; i++ {
		e.expect(e.sendJSON(newBrowser(), http.MethodPost, "/api/public/s/"+s.Token+"/unlock", map[string]string{"password": "guess"}), http.StatusUnauthorized)
	}
	r := e.expect(e.sendJSON(newBrowser(), http.MethodPost, "/api/public/s/"+s.Token+"/unlock", map[string]string{"password": "swordfish"}), http.StatusTooManyRequests)
	wantBody(t, r, "Too many wrong passwords")
	// The command line shares the same limit.
	e.expect(e.get(e.fileURL(s, a), "Link-Password", "swordfish"), http.StatusTooManyRequests)

	expired := e.createShare(nil, a)
	e.changeShare(expired.ID, expireShare)
	e.expect(e.sendJSON(newBrowser(), http.MethodPost, "/api/public/s/"+expired.Token+"/unlock", map[string]string{"password": "x"}), http.StatusNotFound)
}

/* ---------- Download limits ---------- */

func TestDownloadLimit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("aaa"))
	b := e.upload(rootID, "b.txt", []byte("bbb"))
	s := e.createShare(map[string]any{"maxDownloads": 2}, a, b)

	e.expect(e.get(e.fileURL(s, a)), http.StatusOK)
	e.expect(e.get(e.fileURL(s, b)+"?download"), http.StatusOK)
	r := e.expect(e.get(e.fileURL(s, a)), http.StatusGone)
	wantBody(t, r, "the download limit was reached")
	e.expect(e.get("/s/"+s.Token+"/all.zip"), http.StatusGone)
	if got := e.share(s.ID).Downloads; got != 2 {
		t.Errorf("downloads = %d; want 2", got)
	}

	// Raising the limit opens the share again.
	e.expect(e.api(http.MethodPatch, "/api/shares/"+s.ID, map[string]any{"maxDownloads": 3}), http.StatusOK)
	e.expect(e.get(e.fileURL(s, a)), http.StatusOK)
	e.expect(e.get(e.fileURL(s, a)), http.StatusGone)

	// The count is saved, so a restart doesn't hand out new downloads.
	st, err := openStore(e.srv.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.shareByID(s.ID).Downloads; got != 3 {
		t.Errorf("after a restart, downloads = %d; want 3", got)
	}
}

// Checking a link with curl -I, or a link scanner in a mail app doing so, sends no contents and
// doesn't use up a download.
func TestHeadDoesntCount(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("aaa"))
	s := e.createShare(map[string]any{"maxDownloads": 1}, a)

	for i := 0; i < 3; i++ {
		r := e.expect(e.send(newTerminal(), http.MethodHead, e.fileURL(s, a), nil), http.StatusOK)
		if r.header.Get("Content-Length") != "3" {
			t.Errorf("HEAD Content-Length = %q; want 3", r.header.Get("Content-Length"))
		}
		r = e.expect(e.send(newTerminal(), http.MethodHead, "/s/"+s.Token+"/all.zip", nil), http.StatusOK)
		if r.header.Get("Content-Type") != "application/zip" {
			t.Errorf("HEAD on the zip: Content-Type %q", r.header.Get("Content-Type"))
		}
	}
	if got := e.share(s.ID).Downloads; got != 0 {
		t.Errorf("HEAD counted %d downloads", got)
	}
	e.expect(e.get(e.fileURL(s, a)), http.StatusOK)
	e.expect(e.send(newTerminal(), http.MethodHead, e.fileURL(s, a), nil), http.StatusGone)
}

// A Range header can't tell a resume from a new download: anyone can send one. So on its own it
// counts like any other download.
func TestRangeHeaderDoesntGetAroundTheLimit(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.bin", bytes.Repeat([]byte("x"), 5000))
	tests := [][]string{
		{"Range", "bytes=5-"},
		{"Range", "bytes=00-"},
		{"Range", "bytes= 0-"},
		{"Range", "bytes=-99999999"},
		{"Range", "bytes=1-,0-0"},
		{"Range", "bytes=5-", "If-Range", `"stale"`},
	}
	for _, header := range tests {
		s := e.createShare(map[string]any{"maxDownloads": 1}, a)
		if r := e.get(e.fileURL(s, a), header...); r.status != http.StatusOK && r.status != http.StatusPartialContent {
			t.Errorf("%v: first download got %d", header, r.status)
		}
		for i := 0; i < 2; i++ {
			if r := e.get(e.fileURL(s, a), header...); r.status != http.StatusGone {
				t.Errorf("%v: download %d after the limit got %d with %d bytes", header, i+2, r.status, len(r.body))
			}
		}
	}

	zipped := e.createShare(map[string]any{"maxDownloads": 1}, a)
	e.expect(e.get("/s/"+zipped.Token+"/all.zip", "Range", "bytes=5-"), http.StatusOK)
	e.expect(e.get("/s/"+zipped.Token+"/all.zip", "Range", "bytes=5-"), http.StatusGone)
}

// A browser whose download broke off can resume it for a day without counting again, even after
// the limit was reached. Others can't.
func TestResumeFromTheSameBrowser(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	content := make([]byte, 5000)
	rand.Read(content)
	a := e.upload(rootID, "a.bin", content)
	s := e.createShare(map[string]any{"maxDownloads": 1}, a)
	b := newBrowser()

	r := e.expect(e.send(b, http.MethodGet, e.fileURL(s, a), nil), http.StatusOK)
	c := findCookie(r, downloaderCookie)
	if c == nil || c.Path != "/s/"+s.Token+"/" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 86400 || len(c.Value) < 20 {
		t.Fatalf("downloader cookie = %+v", c)
	}

	r = e.expect(e.send(b, http.MethodGet, e.fileURL(s, a), nil, "Range", "bytes=100-"), http.StatusPartialContent)
	if r.body != string(content[100:]) {
		t.Errorf("the resumed part doesn't match: %d bytes", len(r.body))
	}
	e.expect(e.send(b, http.MethodGet, e.fileURL(s, a), nil, "Range", "bytes=4000-4099"), http.StatusPartialContent)
	if got := e.share(s.ID).Downloads; got != 1 {
		t.Errorf("resuming counted: downloads = %d", got)
	}

	// Without that browser's cookie a Range request is a new download, and the limit is reached.
	e.expect(e.get(e.fileURL(s, a), "Range", "bytes=100-"), http.StatusGone)
	e.expect(e.get(e.fileURL(s, a), "Range", "bytes=100-", "Cookie", downloaderCookie+"=made-up"), http.StatusGone)
	e.expect(e.get(e.fileURL(s, a), "Range", "bytes=100-", "Cookie", downloaderCookie+"="+strings.Repeat("x", 65)), http.StatusGone)
	// The same browser starting over is a new download too.
	e.expect(e.send(b, http.MethodGet, e.fileURL(s, a), nil), http.StatusGone)

	// After a day the resume is a new download.
	e.srv.st.mu.Lock()
	for key := range e.srv.resumes {
		e.srv.resumes[key] = time.Now().Add(-time.Second)
	}
	e.srv.st.mu.Unlock()
	e.expect(e.send(b, http.MethodGet, e.fileURL(s, a), nil, "Range", "bytes=100-"), http.StatusGone)
}

// The free resume covers the one file that was counted, not the rest of the share.
func TestResumeCoversOneFile(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("aaaaaaaaaa"))
	b := e.upload(rootID, "b.txt", []byte("bbbbbbbbbb"))
	s := e.createShare(map[string]any{"maxDownloads": 2}, a, b)
	browser := newBrowser()

	e.expect(e.send(browser, http.MethodGet, e.fileURL(s, a), nil), http.StatusOK)
	e.expect(e.send(browser, http.MethodGet, e.fileURL(s, b), nil, "Range", "bytes=5-"), http.StatusPartialContent)
	if got := e.share(s.ID).Downloads; got != 2 {
		t.Errorf("downloads = %d; the first Range request for b.txt should count", got)
	}
	e.expect(e.send(browser, http.MethodGet, e.fileURL(s, b), nil, "Range", "bytes=5-"), http.StatusPartialContent)
	e.expect(e.send(browser, http.MethodGet, "/s/"+s.Token+"/all.zip", nil, "Range", "bytes=5-"), http.StatusGone)
	if got := e.share(s.ID).Downloads; got != 2 {
		t.Errorf("downloads = %d; want 2", got)
	}

	// The zip has its own resume.
	z := e.createShare(map[string]any{"maxDownloads": 1}, a)
	full := e.expect(e.send(browser, http.MethodGet, "/s/"+z.Token+"/all.zip", nil), http.StatusOK)
	r := e.expect(e.send(browser, http.MethodGet, "/s/"+z.Token+"/all.zip", nil, "Range", "bytes=10-"), http.StatusOK)
	if r.body != full.body {
		t.Error("the zip came out different the second time")
	}
	e.expect(e.send(browser, http.MethodGet, e.fileURL(z, a), nil, "Range", "bytes=1-"), http.StatusGone)
}

// A grant to resume doesn't outlive the share being revoked or expiring.
func TestResumeEndsWithTheShare(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("aaaaaaaaaa"))
	browser := newBrowser()

	revoked := e.createShare(map[string]any{"maxDownloads": 1}, a)
	e.expect(e.send(browser, http.MethodGet, e.fileURL(revoked, a), nil), http.StatusOK)
	e.changeShare(revoked.ID, func(s *Share) { s.Revoked = true })
	e.expect(e.send(browser, http.MethodGet, e.fileURL(revoked, a), nil, "Range", "bytes=5-"), http.StatusNotFound)

	expired := e.createShare(map[string]any{"maxDownloads": 1}, a)
	e.expect(e.send(browser, http.MethodGet, e.fileURL(expired, a), nil), http.StatusOK)
	e.changeShare(expired.ID, expireShare)
	e.expect(e.send(browser, http.MethodGet, e.fileURL(expired, a), nil, "Range", "bytes=5-"), http.StatusGone)

	locked := e.createShare(map[string]any{"maxDownloads": 1}, a)
	e.expect(e.send(browser, http.MethodGet, e.fileURL(locked, a), nil), http.StatusOK)
	e.changeShare(locked.ID, func(s *Share) { s.PasswordHash = fastHash("pw") })
	e.expect(e.send(browser, http.MethodGet, e.fileURL(locked, a), nil, "Range", "bytes=5-"), http.StatusUnauthorized)
}

// curl -C - resumes with a Range header. With a cookie jar (-b and -c) it counts as the same
// browser.
func TestCurlResumeWithACookieJar(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	content := make([]byte, 5000)
	rand.Read(content)
	a := e.upload(rootID, "a.bin", content)
	s := e.createShare(map[string]any{"maxDownloads": 1}, a)
	curl := newBrowser()

	// The first attempt breaks off after 1234 bytes.
	req, _ := http.NewRequest(http.MethodGet, e.url+e.fileURL(s, a)+"?download", nil)
	resp, err := curl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part := make([]byte, 1234)
	if _, err := io.ReadFull(resp.Body, part); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	r := e.expect(e.send(curl, http.MethodGet, e.fileURL(s, a)+"?download", nil, "Range", "bytes=1234-"), http.StatusPartialContent)
	if got := append(part, r.body...); !bytes.Equal(got, content) {
		t.Error("the resumed download doesn't match the file")
	}
}

// Several downloads at the same moment get through only as far as the limit allows.
func TestDownloadLimitHoldsUnderConcurrency(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("aaa"))
	s := e.createShare(map[string]any{"maxDownloads": 3}, a)
	var mu sync.Mutex
	counts := map[int]int{}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := newTerminal().Get(e.url + e.fileURL(s, a))
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			mu.Lock()
			counts[resp.StatusCode]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if counts[http.StatusOK] != 3 || counts[http.StatusGone] != 17 {
		t.Errorf("statuses %v; want 3 downloads and 17 refusals", counts)
	}
	if got := e.share(s.ID).Downloads; got != 3 {
		t.Errorf("downloads = %d; want 3", got)
	}
}

/* ---------- Everything as one zip ---------- */

func TestShareZip(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var folder Item
	e.expect(e.api(http.MethodPost, "/api/folders", map[string]string{"parent": rootID, "name": "Other"}), http.StatusCreated).decode(t, &folder)
	a := e.upload(rootID, "a.txt", []byte("first"))
	same1 := e.upload(rootID, "Same.txt", []byte("one"))
	same2 := e.upload(folder.ID, "same.txt", []byte("two"))
	big := make([]byte, 3<<20)
	rand.Read(big)
	bigFile := e.upload(rootID, "big.bin", big)
	s := e.createShare(map[string]any{"name": "Holiday photos 2025!"}, a, same1, same2, bigFile)

	r := e.expect(e.get("/s/"+s.Token+"/all.zip"), http.StatusOK)
	if r.header.Get("Content-Disposition") != "attachment; filename=holiday-photos-2025.zip" {
		t.Errorf("Content-Disposition = %q", r.header.Get("Content-Disposition"))
	}
	got := readZip(t, r.body)
	want := map[string]string{"a.txt": "first", "Same.txt": "one", "2-same.txt": "two", "big.bin": string(big)}
	if len(got) != len(want) {
		t.Errorf("zip holds %d files; want %d", len(got), len(want))
	}
	for name, content := range want {
		if got[name] != content {
			t.Errorf("%s in the zip: %d bytes; want %d", name, len(got[name]), len(content))
		}
	}
	if got := e.share(s.ID).Downloads; got != 1 {
		t.Errorf("downloads = %d; the zip counts once", got)
	}
}

// A file whose contents went missing doesn't spoil the zip for the others.
func TestShareZipSkipsMissingFiles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("aaa"))
	b := e.upload(rootID, "b.txt", []byte("bbb"))
	s := e.createShare(nil, a, b)
	e.expect(e.api(http.MethodPost, "/api/items/delete", map[string]any{"ids": []string{a.ID}}), http.StatusOK)

	got := readZip(t, e.expect(e.get("/s/"+s.Token+"/all.zip"), http.StatusOK).body)
	if len(got) != 1 || got["b.txt"] != "bbb" {
		t.Errorf("zip = %v", got)
	}

	c := e.upload(rootID, "c.txt", []byte("ccc"))
	s2 := e.createShare(nil, b, c)
	if err := os.Remove(e.srv.st.blobPath(b.ID)); err != nil {
		t.Fatal(err)
	}
	got = readZip(t, e.expect(e.get("/s/"+s2.Token+"/all.zip"), http.StatusOK).body)
	if len(got) != 1 || got["c.txt"] != "ccc" {
		t.Errorf("zip = %v", got)
	}
}

func readZip(t *testing.T, body string) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		if f.Method != zip.Store {
			t.Errorf("%s is compressed; files are stored as they are", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("%s: %v", f.Name, err)
		}
		out[f.Name] = string(raw)
	}
	return out
}

// The share page and terminal instructions use the public URL when one is set.
func TestShareLinksUseThePublicURL(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, config{PublicURL: "https://files.example.com"})
	a := e.upload(rootID, "a.txt", []byte("a"))
	s := e.createShare(nil, a)
	r := e.expect(e.get("/s/"+s.Token), http.StatusOK)
	wantBody(t, r, "https://files.example.com/s/"+s.Token+"/all.zip")
}
