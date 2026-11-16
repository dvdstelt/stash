package main

// Chat apps such as WhatsApp, Slack and Teams show a preview of a pasted link, built from the Open
// Graph tags in its page. The preview holds the link's name, or says the link can't be used, and
// nothing the page keeps behind the password.

import (
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

const whatsApp = "WhatsApp/2.24.6.77 A"

var (
	ogTitle       = regexp.MustCompile(`<meta property="og:title" content="([^"]*)">`)
	ogDescription = regexp.MustCompile(`<meta property="og:description" content="([^"]*)">`)
	pageTitle     = regexp.MustCompile(`<title>([^<]*)</title>`)
)

// preview fetches a link's page the way WhatsApp does, and returns what its preview shows.
func (e *testEnv) preview(path string) (title, description string, page reply) {
	e.t.Helper()
	page = e.expect(e.get(path, "User-Agent", whatsApp, "Accept", "*/*"), http.StatusOK)
	if ct := page.header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		e.t.Fatalf("%s: a chat app gets %q, not the page", path, ct)
	}
	t, d := ogTitle.FindStringSubmatch(page.body), ogDescription.FindStringSubmatch(page.body)
	if t == nil || d == nil {
		e.t.Fatalf("%s: the page has no og:title or og:description:\n%s", path, page.body)
	}
	title, description = html.UnescapeString(t[1]), html.UnescapeString(d[1])
	if pt := pageTitle.FindStringSubmatch(page.body); pt == nil || html.UnescapeString(pt[1]) != title+" · Stash" {
		e.t.Errorf("%s: the page's title is %q; want %q", path, pt, title+" · Stash")
	}
	return title, description, page
}

func TestLinkPreviews(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := e.upload(rootID, "beach-secret.jpg", []byte("photo"))
	share := func(fields map[string]any) shareView {
		fields["message"] = "A private note"
		return e.createShare(fields, f)
	}
	link := func(fields map[string]any) linkView {
		fields["message"] = "A private note"
		return e.createLink(fields)
	}
	open := share(map[string]any{"name": "Holiday photos"})
	locked := share(map[string]any{"name": "Tax papers", "password": "letmein"})
	expired := share(map[string]any{"name": "Old share", "password": "letmein"})
	e.changeShare(expired.ID, expireShare)
	full := share(map[string]any{"name": "Used up", "password": "letmein", "maxDownloads": 1})
	e.changeShare(full.ID, func(s *Share) { s.Downloads = 1 })
	revoked := share(map[string]any{"name": "Revoked share"})
	e.changeShare(revoked.ID, func(s *Share) { s.Revoked = true })

	openLink := link(map[string]any{"name": "Wedding photos"})
	lockedLink := link(map[string]any{"name": "Contracts", "password": "letmein"})
	expiredLink := link(map[string]any{"name": "Old link"})
	e.changeLink(expiredLink.ID, expireLink)
	revokedLink := link(map[string]any{"name": "Revoked link"})
	e.changeLink(revokedLink.ID, func(l *Link) { l.Revoked = true })

	tests := []struct {
		path, title, description string
	}{
		{"/s/" + open.Token, "Holiday photos", "Open the link to see and download the files."},
		{"/s/" + locked.Token, "Tax papers", "Open the link to see and download the files."},
		{"/s/" + expired.Token, "This share has expired", "Ask the person who sent it for a new link."},
		{"/s/" + full.Token, "Download limit reached", "These files can’t be downloaded anymore. Ask the person who sent the link to share them again."},
		{"/s/" + revoked.Token, "This link doesn’t work", "Ask the person who sent it for a new link."},
		{"/s/made-up", "This link doesn’t work", "Ask the person who sent it for a new link."},
		{"/d/" + openLink.Token, "Wedding photos", "Open the link to send your files."},
		{"/d/" + lockedLink.Token, "Contracts", "Open the link to send your files."},
		{"/d/" + expiredLink.Token, "This upload link has expired", "If you still need to send something, ask for a new link."},
		{"/d/" + revokedLink.Token, "This link doesn’t work", "Ask the person who sent it for a new link."},
		{"/d/made-up", "This link doesn’t work", "Ask the person who sent it for a new link."},
	}
	for _, tt := range tests {
		title, description, page := e.preview(tt.path)
		if title != tt.title || description != tt.description {
			t.Errorf("%s: preview %q, %q; want %q, %q", tt.path, title, description, tt.title, tt.description)
		}
		// Only the name: no message, no file names, no sizes, also for links without a password.
		wantNoBody(t, page, "A private note", "beach-secret", "Old share", "Used up", "Revoked share", "Old link", "Revoked link")
		// The page itself is what any browser gets.
		browser := e.expect(e.get(tt.path, "Accept", "text/html"), http.StatusOK)
		if browser.body != page.body || browser.header.Get("Content-Security-Policy") != pageCSP {
			t.Errorf("%s: a browser gets a different page than a chat app", tt.path)
		}
	}
}

// Slack, Teams and others don't all ask for HTML. They get the page with its preview, rather than
// the instructions for a terminal, which list the files of a share without a password.
func TestChatAppsGetThePage(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := e.upload(rootID, "listed.txt", []byte("hello"))
	s := e.createShare(map[string]any{"name": "Holiday photos"}, f)
	l := e.createLink(map[string]any{"name": "Wedding photos"})

	for _, ua := range []string{
		whatsApp,
		"facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)",
		"facebookexternalhit/1.1 Facebot Twitterbot/1.0", // iMessage
		"Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)",
		"Mozilla/5.0 (Windows NT 6.1; WOW64) SkypeUriPreview Preview/0.5 skype-url-preview@microsoft.com", // Teams
		"Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com)",
		"TelegramBot (like TwitterBot)",
	} {
		for path, name := range map[string]string{"/s/" + s.Token: "Holiday photos", "/d/" + l.Token: "Wedding photos"} {
			r := e.expect(e.get(path, "User-Agent", ua, "Accept", "*/*"), http.StatusOK)
			if r.header.Get("Content-Type") != "text/html; charset=utf-8" {
				t.Errorf("%s gets %q for %s", ua, r.header.Get("Content-Type"), path)
			}
			wantBody(t, r, `<meta property="og:title" content="`+name+`">`)
			wantNoBody(t, r, "listed.txt", "curl")
		}
	}

	for _, ua := range []string{"curl/8.7.1", "Wget/1.21.4", "Mozilla/5.0 (Windows NT 10.0; Microsoft Windows 10.0.22631; en-US) PowerShell/7.4.6"} {
		r := e.expect(e.get("/s/"+s.Token, "User-Agent", ua, "Accept", "*/*"), http.StatusOK)
		if !strings.HasPrefix(r.header.Get("Content-Type"), "text/plain") {
			t.Errorf("%s gets %q", ua, r.header.Get("Content-Type"))
		}
		wantBody(t, r, "listed.txt", "curl -L -o")
		r = e.expect(e.get("/d/"+l.Token, "User-Agent", ua, "Accept", "*/*"), http.StatusOK)
		wantBody(t, r, "This is a Stash upload link.")
	}
}

// Fetching a preview is no download, so a link that may be downloaded once still works for the
// person it was sent to. It's no password attempt either.
func TestPreviewsCountNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := e.upload(rootID, "once.txt", []byte("only once"))
	s := e.createShare(map[string]any{"name": "Once", "maxDownloads": 1}, f)
	locked := e.createShare(map[string]any{"name": "Locked", "password": "letmein"}, f)

	for range 3 {
		e.preview("/s/" + s.Token)
		e.preview("/s/" + locked.Token)
	}
	if got := e.share(s.ID).Downloads; got != 0 {
		t.Fatalf("previews counted %d downloads", got)
	}
	e.srv.limits.mu.Lock()
	fails := len(e.srv.limits.fails)
	e.srv.limits.mu.Unlock()
	if fails != 0 {
		t.Errorf("previews counted as %d failed password attempts", fails)
	}
	r := e.expect(e.get(e.fileURL(s, f)+"?download"), http.StatusOK)
	if r.body != "only once" {
		t.Errorf("the download after the previews got %q", r.body)
	}
}

// The owner chooses the name, but it's still escaped, so it can't add anything to the page.
func TestPreviewEscapesTheName(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	name := `Tom & Jerry "><script>alert(1)</script>`
	f := e.upload(rootID, "a.txt", []byte("a"))
	s := e.createShare(map[string]any{"name": name}, f)
	l := e.createLink(map[string]any{"name": name})
	for _, path := range []string{"/s/" + s.Token, "/d/" + l.Token} {
		title, _, page := e.preview(path)
		if title != name {
			t.Errorf("%s: og:title %q; want %q", path, title, name)
		}
		wantNoBody(t, page, "<script>alert")
	}
}
