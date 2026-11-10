package main

import (
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Only a short list of file kinds opens in the browser, with a fixed type and a sandbox.
// Everything else, web pages and SVG included, downloads.
func TestServeFileOpensOnlySafeKinds(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(nil)
	tests := []struct {
		name     string
		viaLink  bool // received through an upload link
		ctype    string
		inline   bool
		pdfFrame bool
	}{
		{"photo.png", false, "image/png", true, false},
		{"PHOTO.JPG", false, "image/jpeg", true, false},
		{"clip.mp4", false, "video/mp4", true, false},
		{"notes.txt", false, "text/plain; charset=utf-8", true, false},
		{"data.json", false, "text/plain; charset=utf-8", true, false},
		{"feed.xml", false, "text/plain; charset=utf-8", true, false},
		{"page.html", false, "application/octet-stream", false, false},
		{"page.HTM", false, "application/octet-stream", false, false},
		{"drawing.svg", false, "application/octet-stream", false, false},
		{"script.js", false, "application/octet-stream", false, false},
		{"archive.zip", false, "application/octet-stream", false, false},
		{"no-extension", false, "application/octet-stream", false, false},
		{"report.pdf", false, "application/pdf", true, true},
		{"from-a-stranger.pdf", true, "application/octet-stream", false, false},
		{"from-a-stranger.png", true, "image/png", true, false},
		{"from-a-stranger.html", true, "application/octet-stream", false, false},
	}
	for _, tt := range tests {
		var f Item
		if tt.viaLink {
			e.expect(e.put(l.Token, tt.name, strings.NewReader("<script>alert(1)</script>")), http.StatusOK)
			f = e.filesIn(l.Folder)[tt.name]
		} else {
			f = e.upload(rootID, tt.name, []byte("<script>alert(1)</script>"))
		}
		s := e.createShare(nil, f)
		for _, path := range []string{"/api/files/" + f.ID + "/content", e.fileURL(s, f)} {
			r := e.expect(e.send(e.owner, http.MethodGet, path, nil), http.StatusOK)
			h := r.header
			disposition, params, err := mime.ParseMediaType(h.Get("Content-Disposition"))
			if err != nil || params["filename"] != tt.name {
				t.Errorf("%s: Content-Disposition %q", tt.name, h.Get("Content-Disposition"))
			}
			if h.Get("Content-Type") != tt.ctype || (disposition == "inline") != tt.inline {
				t.Errorf("%s via %s: %q, %s; want %q, inline %v", tt.name, path, h.Get("Content-Type"), disposition, tt.ctype, tt.inline)
			}
			csp := h.Get("Content-Security-Policy")
			if tt.pdfFrame {
				if csp != "frame-ancestors 'self'" {
					t.Errorf("%s: CSP %q", tt.name, csp)
				}
			} else if !strings.HasPrefix(csp, "sandbox; default-src 'none'") {
				t.Errorf("%s: CSP %q; want a sandbox", tt.name, csp)
			}
			if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "private, no-cache" {
				t.Errorf("%s: nosniff %q, Cache-Control %q", tt.name, h.Get("X-Content-Type-Options"), h.Get("Cache-Control"))
			}
		}
	}
}

// File names go into a header. Quotes, line breaks and non-ASCII letters can't break out of it.
func TestServeFileNameInHeader(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for _, name := range []string{`say "hi".txt`, "naïve café.txt", "semi;colon=.txt", "日本語.txt", `back\slash.txt`} {
		f := e.upload(rootID, name, []byte("x"))
		r := e.expect(e.send(e.owner, http.MethodGet, "/api/files/"+f.ID+"/content?download", nil), http.StatusOK)
		disposition, params, err := mime.ParseMediaType(r.header.Get("Content-Disposition"))
		if err != nil || disposition != "attachment" || params["filename"] != f.Name {
			t.Errorf("%q: Content-Disposition %q gives %q, %v", f.Name, r.header.Get("Content-Disposition"), params["filename"], err)
		}
	}
}

func TestEveryReplyKeepsTokensPrivate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(nil)
	for _, path := range []string{"/", "/login", "/d/" + l.Token, "/s/made-up", "/api/session", "/api/public/d/" + l.Token, "/js/ui.js", "/nope"} {
		r := e.get(path)
		if r.header.Get("Referrer-Policy") != "no-referrer" || r.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: Referrer-Policy %q, X-Content-Type-Options %q", path, r.header.Get("Referrer-Policy"), r.header.Get("X-Content-Type-Options"))
		}
	}
}

func TestPages(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	r := e.expect(e.get("/"), http.StatusSeeOther)
	if r.header.Get("Location") != "/login" {
		t.Errorf("signed out, / redirects to %q", r.header.Get("Location"))
	}
	for path, client := range map[string]*http.Client{"/": e.owner, "/login": newTerminal(), "/d/x": nil, "/s/x": nil} {
		var r reply
		if client == nil {
			r = e.get(path, "Accept", "text/html")
		} else {
			r = e.send(client, http.MethodGet, path, nil)
		}
		e.expect(r, http.StatusOK)
		h := r.header
		if h.Get("Content-Type") != "text/html; charset=utf-8" || h.Get("Content-Security-Policy") != pageCSP || h.Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %q, CSP %q, Cache-Control %q", path, h.Get("Content-Type"), h.Get("Content-Security-Policy"), h.Get("Cache-Control"))
		}
	}
	if !strings.Contains(pageCSP, "script-src 'self'") || strings.Contains(pageCSP, "unsafe-eval") || !strings.Contains(pageCSP, "frame-ancestors 'none'") {
		t.Errorf("page CSP = %q", pageCSP)
	}

	for _, path := range []string{"/css/", "/js/", "/nope", "/js/nope.js"} {
		e.expect(e.get(path), http.StatusNotFound)
	}
}

// Every script and stylesheet a page loads is there.
func TestPagesLoadOnlyFilesThatExist(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ref := regexp.MustCompile(`(?:src|href)="(/(?:js|css)/[^"]+)"`)
	pages, _ := fs.Glob(e.srv.ui, "*.html")
	if len(pages) < 4 {
		t.Fatalf("found %d pages", len(pages))
	}
	for _, page := range pages {
		raw, err := fs.ReadFile(e.srv.ui, page)
		if err != nil {
			t.Fatal(err)
		}
		refs := ref.FindAllStringSubmatch(string(raw), -1)
		if len(refs) == 0 {
			t.Errorf("%s loads no scripts or stylesheets; did the paths change?", page)
		}
		for _, m := range refs {
			r := e.get(m[1])
			if r.status != http.StatusOK {
				t.Errorf("%s loads %s, which gives %d", page, m[1], r.status)
			}
			if r.header.Get("Cache-Control") != "no-cache" {
				t.Errorf("%s: Cache-Control %q", m[1], r.header.Get("Cache-Control"))
			}
		}
	}
}

// The pages' Content-Security-Policy blocks inline scripts and event handlers, so a page that
// uses one is broken.
func TestPagesHaveNoInlineScripts(t *testing.T) {
	t.Parallel()
	ui, err := fs.Sub(embeddedUI, "ui")
	if err != nil {
		t.Fatal(err)
	}
	scriptTag := regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	handler := regexp.MustCompile(`(?i)<[^>]*\son[a-z]+\s*=`)
	jsURL := regexp.MustCompile(`(?i)(?:href|src)\s*=\s*["']?\s*javascript:`)
	pages, _ := fs.Glob(ui, "*.html")
	for _, page := range pages {
		raw, _ := fs.ReadFile(ui, page)
		html := string(raw)
		for _, m := range scriptTag.FindAllStringSubmatch(html, -1) {
			if !strings.Contains(m[1], "src=") || strings.TrimSpace(m[2]) != "" {
				t.Errorf("%s has an inline script: %.80s", page, m[0])
			}
		}
		if m := handler.FindString(html); m != "" {
			t.Errorf("%s has an inline event handler: %.80s", page, m)
		}
		if m := jsURL.FindString(html); m != "" {
			t.Errorf("%s has a javascript: URL: %s", page, m)
		}
	}
}

// With -ui-dir the pages come from disk, so page edits show without rebuilding.
func TestUIDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "login.html"), []byte("<p>from disk</p>"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := startServer(t, config{UIDir: dir})
	if r := e.expect(e.get("/login"), http.StatusOK); r.body != "<p>from disk</p>" {
		t.Errorf("login page = %q", r.body)
	}
	e.expect(e.get("/d/x", "Accept", "text/html"), http.StatusNotFound)
}
