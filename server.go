package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed ui
var embeddedUI embed.FS

type server struct {
	cfg        config
	st         *store
	ui         fs.FS
	sessions   *sessions
	limits     *limiter
	setupToken string // set while no owner password exists; guarded by st.mu

	// Uploads in progress, and the space set aside for them. Guarded by st.mu.
	uploads  map[string]*upload
	reserved map[string]int64 // per upload link: bytes set aside
	pending  map[string]int   // per upload link: files in progress
	inflight int64            // all uploads together: bytes set aside
}

func newServer(cfg config) (*server, error) {
	st, err := openStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	// Leftovers of uploads interrupted by a restart can't be resumed, so clear them out.
	if parts, err := os.ReadDir(filepath.Join(cfg.DataDir, "uploads")); err == nil {
		for _, p := range parts {
			os.Remove(filepath.Join(cfg.DataDir, "uploads", p.Name()))
		}
	}

	ui, err := fs.Sub(embeddedUI, "ui")
	if err != nil {
		return nil, err
	}
	if cfg.UIDir != "" {
		ui = os.DirFS(cfg.UIDir)
	}

	srv := &server{
		cfg: cfg, st: st, ui: ui,
		sessions: &sessions{m: map[string]time.Time{}},
		limits:   &limiter{fails: map[string]*failures{}},
		uploads:  map[string]*upload{},
		reserved: map[string]int64{},
		pending:  map[string]int{},
	}
	if st.ownerHash == "" {
		srv.setupToken = newID()
		base := cfg.PublicURL
		if base == "" {
			base = "http://localhost"
			if _, port, err := net.SplitHostPort(cfg.Listen); err == nil && port != "80" {
				base += ":" + port
			}
		}
		log.Printf("No password is set yet. Choose one here: %s/login?setup=%s", base, srv.setupToken)
	}
	return srv, nil
}

func (srv *server) routes() http.Handler {
	mux := http.NewServeMux()

	// Pages
	mux.HandleFunc("GET /{$}", srv.handleHome)
	mux.HandleFunc("GET /login", srv.handleLoginPage)
	mux.HandleFunc("GET /d/{token}", srv.handleUploadPage)
	mux.HandleFunc("GET /s/{token}", srv.handleSharePage)
	mux.Handle("GET /css/", srv.static())
	mux.Handle("GET /js/", srv.static())

	// Signing in
	mux.HandleFunc("GET /api/session", srv.handleSession)
	mux.HandleFunc("POST /api/setup", srv.handleSetup)
	mux.HandleFunc("POST /api/login", srv.handleLogin)
	mux.HandleFunc("POST /api/logout", srv.handleLogout)

	// Owner: files, upload links, share links
	mux.HandleFunc("GET /api/state", srv.owner(srv.handleState))
	mux.HandleFunc("POST /api/folders", srv.owner(srv.handleCreateFolder))
	mux.HandleFunc("POST /api/items/delete", srv.owner(srv.handleDeleteItems))
	mux.HandleFunc("GET /api/files/{id}/content", srv.owner(srv.handleFileContent))
	mux.HandleFunc("POST /api/uploads", srv.owner(srv.handleOwnerUploadStart))
	mux.HandleFunc("GET /api/uploads/{id}", srv.owner(srv.ownerUpload(srv.handleUploadStatus)))
	mux.HandleFunc("PUT /api/uploads/{id}", srv.owner(srv.ownerUpload(srv.handleUploadChunk)))
	mux.HandleFunc("DELETE /api/uploads/{id}", srv.owner(srv.ownerUpload(srv.handleUploadCancel)))
	mux.HandleFunc("POST /api/links", srv.owner(srv.handleCreateLink))
	mux.HandleFunc("PATCH /api/links/{id}", srv.owner(srv.handleUpdateLink))
	mux.HandleFunc("POST /api/shares", srv.owner(srv.handleCreateShare))
	mux.HandleFunc("PATCH /api/shares/{id}", srv.owner(srv.handleUpdateShare))

	// Anyone with an upload link
	mux.HandleFunc("GET /api/public/d/{token}", srv.handlePublicLink)
	mux.HandleFunc("POST /api/public/d/{token}/unlock", srv.handleLinkUnlock)
	mux.HandleFunc("POST /api/public/d/{token}/uploads", srv.handlePublicUploadStart)
	mux.HandleFunc("GET /api/public/d/{token}/uploads/{id}", srv.publicUpload(srv.handleUploadStatus))
	mux.HandleFunc("PUT /api/public/d/{token}/uploads/{id}", srv.publicUpload(srv.handleUploadChunk))
	mux.HandleFunc("DELETE /api/public/d/{token}/uploads/{id}", srv.publicUpload(srv.handleUploadCancel))
	mux.HandleFunc("PUT /d/{token}/{name}", srv.handleConsoleUpload)

	// Anyone with a share link
	mux.HandleFunc("GET /api/public/s/{token}", srv.handlePublicShare)
	mux.HandleFunc("POST /api/public/s/{token}/unlock", srv.handleShareUnlock)
	mux.HandleFunc("GET /s/{token}/files/{id}", srv.handleShareFile)
	mux.HandleFunc("GET /s/{token}/all.zip", srv.handleShareZip)

	return baseHeaders(mux)
}

// baseHeaders applies to every response. Links carry secret tokens in their URL, so the
// Referer header must never pass them on to another site.
func baseHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

/* ---------- Pages and static files ---------- */

const pageCSP = "default-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:; frame-src 'self' blob:; " +
	"style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; " +
	"form-action 'self'; frame-ancestors 'none'"

func (srv *server) servePage(w http.ResponseWriter, name string) {
	raw, err := fs.ReadFile(srv.ui, name)
	if err != nil {
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", pageCSP)
	h.Set("Cache-Control", "no-cache")
	w.Write(raw)
}

func (srv *server) static() http.Handler {
	files := http.FileServerFS(srv.ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") { // no directory listings
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

func (srv *server) handleHome(w http.ResponseWriter, r *http.Request) {
	if !srv.signedIn(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	srv.servePage(w, "index.html")
}

func (srv *server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	srv.servePage(w, "login.html")
}

func wantsHTML(r *http.Request) bool { return strings.Contains(r.Header.Get("Accept"), "text/html") }

/* ---------- Signing in ---------- */

func (srv *server) handleSession(w http.ResponseWriter, r *http.Request) {
	srv.st.mu.Lock()
	setup := srv.st.ownerHash == ""
	srv.st.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"signedIn": srv.signedIn(r), "setupRequired": setup})
}

func (srv *server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var in struct{ Token, Password string }
	if !readJSON(w, r, &in) {
		return
	}
	if len([]rune(in.Password)) < 10 {
		jsonError(w, http.StatusBadRequest, "Use at least 10 characters.")
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "Couldn't store the password.")
		return
	}
	srv.st.mu.Lock()
	switch {
	case srv.st.ownerHash != "":
		srv.st.mu.Unlock()
		jsonError(w, http.StatusConflict, "A password is already set. Sign in instead.")
		return
	case srv.setupToken == "" || !tokenEqual(in.Token, srv.setupToken):
		srv.st.mu.Unlock()
		jsonError(w, http.StatusForbidden, "This setup link isn't valid. Use the link Stash printed when it started.")
		return
	}
	srv.st.ownerHash, srv.setupToken = hash, ""
	err = srv.st.save()
	srv.st.mu.Unlock()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "Couldn't store the password.")
		return
	}
	srv.startSession(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (srv *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Password string }
	if !readJSON(w, r, &in) {
		return
	}
	key := "login|" + srv.clientIP(r).String()
	if wait := srv.limits.wait(key); wait > 0 {
		jsonError(w, http.StatusTooManyRequests, fmt.Sprintf("Too many attempts. Try again in %s.", wait.Round(time.Second)))
		return
	}
	srv.st.mu.Lock()
	hash := srv.st.ownerHash
	srv.st.mu.Unlock()
	if hash == "" || !checkPassword(hash, in.Password) {
		srv.limits.fail(key)
		jsonError(w, http.StatusUnauthorized, "That password isn't right.")
		return
	}
	srv.limits.succeed(key)
	srv.startSession(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (srv *server) startSession(w http.ResponseWriter, r *http.Request) {
	token, expires := srv.sessions.create()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: srv.isHTTPS(r), SameSite: http.SameSiteStrictMode,
	})
}

func (srv *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		srv.sessions.remove(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// owner only lets the signed-in owner through. Changes also need the X-Stash header, which
// other sites can't add to a request without the browser asking this server first.
func (srv *server) owner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !srv.signedIn(r) {
			jsonError(w, http.StatusUnauthorized, "Sign in first.")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-Stash") != "1" {
			jsonError(w, http.StatusForbidden, "Missing X-Stash header.")
			return
		}
		next(w, r)
	}
}

/* ---------- Serving file contents ---------- */

// Only these kinds of files open in the browser; everything else downloads. They get a fixed
// Content-Type so the browser can't be talked into treating an upload as a web page.
var inlineTypes = map[string]string{
	".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".png": "image/png", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp",
	".pdf": "application/pdf",
	".mp4": "video/mp4", ".webm": "video/webm", ".mov": "video/quicktime",
	".txt": "text/plain; charset=utf-8", ".md": "text/plain; charset=utf-8", ".log": "text/plain; charset=utf-8",
	".csv": "text/plain; charset=utf-8", ".json": "text/plain; charset=utf-8", ".xml": "text/plain; charset=utf-8",
	".yaml": "text/plain; charset=utf-8", ".yml": "text/plain; charset=utf-8",
}

func (srv *server) serveFile(w http.ResponseWriter, r *http.Request, it *Item, download bool) {
	f, err := os.Open(srv.st.blobPath(it.ID))
	if err != nil {
		http.Error(w, "The file is missing on the server.", http.StatusNotFound)
		return
	}
	defer f.Close()

	h := w.Header()
	ctype, inline := inlineTypes[strings.ToLower(filepath.Ext(it.Name))]
	disposition := "inline"
	if download || !inline {
		ctype, disposition = "application/octet-stream", "attachment"
	}
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": it.Name}))
	h.Set("Cache-Control", "private, no-cache")
	if ctype == "application/pdf" {
		h.Set("Content-Security-Policy", "frame-ancestors 'self'") // the browser's PDF viewer doesn't work sandboxed
	} else {
		h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'; frame-ancestors 'self'")
	}
	http.ServeContent(w, r, "", time.UnixMilli(it.Modified), f)
}

/* ---------- Housekeeping ---------- */

// janitor drops expired sign-ins, old password failures and uploads nobody finished in a day.
func (srv *server) janitor(ctx context.Context) {
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		srv.sessions.prune()
		srv.limits.prune()
		srv.st.mu.Lock()
		var stale []*upload
		for _, u := range srv.uploads {
			if time.Since(u.lastActive()) > 24*time.Hour {
				stale = append(stale, u)
			}
		}
		srv.st.mu.Unlock()
		for _, u := range stale {
			// A chunk that is still arriving holds the lock; that upload isn't stale, so skip it.
			if u.mu.TryLock() {
				srv.cancelLocked(u)
				u.mu.Unlock()
			}
		}
	}
}

/* ---------- JSON helpers ---------- */

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// readJSON decodes a small JSON request body, answering with an error itself when that fails.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			jsonError(w, http.StatusRequestEntityTooLarge, "The request is too large.")
		} else {
			jsonError(w, http.StatusBadRequest, "The request isn't valid JSON: "+err.Error())
		}
		return false
	}
	if dec.More() {
		jsonError(w, http.StatusBadRequest, "The request has extra data after the JSON.")
		return false
	}
	return true
}

func textResponse(w http.ResponseWriter, status int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	io.WriteString(w, text)
}

// formatBytes matches how the web pages show sizes: 1000-based, one decimal below 100.
func formatBytes(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	v := float64(n)
	i := -1
	for v >= 999.95 && i < len(units)-1 {
		v /= 1000
		i++
	}
	if v >= 100 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return strings.Replace(fmt.Sprintf("%.1f %s", v, units[i]), ".0 ", " ", 1)
}
