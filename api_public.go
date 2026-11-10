package main

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

/* ---------- Upload links ---------- */

// handleUploadPage shows the upload page in a browser, and short instructions to anyone opening
// the link from a terminal (curl sends Accept: */*). The instructions don't depend on the link,
// so they reveal nothing about it.
func (srv *server) handleUploadPage(w http.ResponseWriter, r *http.Request) {
	if wantsHTML(r) {
		srv.servePage(w, "upload.html")
		return
	}
	url := srv.origin(r) + "/d/" + r.PathValue("token") + "/"
	textResponse(w, http.StatusOK, fmt.Sprintf(`This is a Stash upload link. Upload a file with:

  macOS, Linux:  curl -T ./file.zip %[1]s
  Windows:       curl.exe -T .\file.zip %[1]s
  PowerShell 7:  Invoke-RestMethod -Method Put -InFile .\file.zip -Uri %[1]sfile.zip

The file keeps its name. If the link has a password, add -H "Link-Password: YOUR-PASSWORD".
`, url))
}

// publicLink finds the upload link in the URL. It answers itself when the link is gone,
// expired, or still locked behind its password.
func (srv *server) publicLink(w http.ResponseWriter, r *http.Request) (*Link, bool) {
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	l := srv.st.linkByToken(r.PathValue("token"))
	switch {
	case l == nil || l.Revoked:
		writeJSON(w, http.StatusNotFound, map[string]string{"status": "gone", "error": "This link doesn't work."})
		return nil, false
	case l.Expires != 0 && l.Expires <= nowMillis():
		writeJSON(w, http.StatusGone, map[string]any{"status": "expired", "expires": l.Expires, "error": "This link has expired."})
		return nil, false
	case !srv.isUnlocked(r, "link", l.ID, l.PasswordHash):
		writeJSON(w, http.StatusUnauthorized, map[string]any{"status": "active", "locked": true, "name": l.Name, "error": "Enter the password first."})
		return nil, false
	}
	return l, true
}

func (srv *server) handlePublicLink(w http.ResponseWriter, r *http.Request) {
	l, ok := srv.publicLink(w, r)
	if !ok {
		return
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	used := srv.reserved[l.ID]
	stored, files := srv.st.linkUsage(l)
	used += stored
	writeJSON(w, http.StatusOK, map[string]any{
		"status": linkStatus(l, used, files+srv.pending[l.ID]), "name": l.Name, "message": l.Message,
		"quota": l.Quota, "used": used, "files": files + srv.pending[l.ID], "maxFiles": l.MaxFiles, "expires": l.Expires,
		"password": l.PasswordHash != "",
	})
}

func (srv *server) handleLinkUnlock(w http.ResponseWriter, r *http.Request) {
	srv.st.mu.Lock()
	l := srv.st.linkByToken(r.PathValue("token"))
	var id, hash string
	if l != nil && !l.Revoked {
		id, hash = l.ID, l.PasswordHash
	}
	srv.st.mu.Unlock()
	if id == "" {
		jsonError(w, http.StatusNotFound, "This link doesn't work.")
		return
	}
	srv.unlock(w, r, "link", id, hash)
}

func (srv *server) unlock(w http.ResponseWriter, r *http.Request, kind, id, hash string) {
	var in struct{ Password string }
	if !readJSON(w, r, &in) {
		return
	}
	key := kind + ":" + id + "|" + srv.visitor(r)
	if wait := srv.limits.wait(key); wait > 0 {
		jsonError(w, http.StatusTooManyRequests, fmt.Sprintf("Too many wrong passwords. Try again in %s.", wait.Round(time.Second)))
		return
	}
	if hash != "" {
		ok, err := checkPassword(hash, in.Password)
		if err != nil {
			status, msg := hashProblem(err, "Couldn't check the password.")
			jsonError(w, status, msg)
			return
		}
		if !ok {
			srv.wrongPassword(r, key, kind, id)
			jsonError(w, http.StatusUnauthorized, "That password isn't right.")
			return
		}
	}
	srv.limits.succeed(key)
	srv.setUnlocked(w, r, kind, id, hash)
	w.WriteHeader(http.StatusNoContent)
}

func (srv *server) handlePublicUploadStart(w http.ResponseWriter, r *http.Request) {
	l, ok := srv.publicLink(w, r)
	if !ok {
		return
	}
	var in struct {
		Name string
		Size int64
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Size < 0 {
		jsonError(w, http.StatusBadRequest, "The size can't be negative.")
		return
	}
	u, err := srv.startUpload(in.Name, l.Folder, l, in.Size)
	if err != nil {
		status, msg := errorStatus(err)
		jsonError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": u.id})
}

// publicUpload lets chunks through only for an upload that was started through the same link.
// An upload that already started may finish even if the link expires in the meantime.
func (srv *server) publicUpload(next uploadHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		srv.st.mu.Lock()
		l := srv.st.linkByToken(r.PathValue("token"))
		var id, hash string
		if l != nil && !l.Revoked {
			id, hash = l.ID, l.PasswordHash
		}
		srv.st.mu.Unlock()
		if id == "" || !srv.isUnlocked(r, "link", id, hash) {
			jsonError(w, http.StatusNotFound, "This link doesn't work.")
			return
		}
		if u := srv.findUpload(w, r, id); u != nil {
			next(w, r, u)
		}
	}
}

/* ---------- Share links ---------- */

// shareFor finds the share link in the URL, if it can still be used.
func (srv *server) shareFor(r *http.Request) (s Share, status string) {
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	found := srv.st.shareByToken(r.PathValue("token"))
	if found == nil || found.Revoked {
		return Share{}, "gone"
	}
	s = *found
	s.Files = append([]string(nil), found.Files...)
	return s, shareStatus(found)
}

func (srv *server) shareFiles(s *Share) []Item {
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	var out []Item
	for _, id := range s.Files {
		if it := srv.st.items[id]; it != nil {
			out = append(out, *it)
		}
	}
	return out
}

func (srv *server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	if wantsHTML(r) {
		srv.servePage(w, "share.html")
		return
	}
	s, status := srv.shareFor(r)
	if shareClosed(w, status) || !srv.passwordOK(w, r, "share", s.ID, s.PasswordHash) {
		return
	}
	base := srv.origin(r) + "/s/" + r.PathValue("token")
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nDownload everything as one zip file:\n  curl -L -o %s.zip %s/all.zip\n\nOr one file at a time:\n", s.Name, slugify(s.Name), base)
	for _, it := range srv.shareFiles(&s) {
		fmt.Fprintf(&b, "  %s (%s)\n    curl -L -o %s '%s/files/%s?download'\n", it.Name, formatBytes(it.Size), shellQuote(it.Name), base, it.ID)
	}
	textResponse(w, http.StatusOK, b.String())
}

func (srv *server) handlePublicShare(w http.ResponseWriter, r *http.Request) {
	s, status := srv.shareFor(r)
	switch {
	case status == "gone":
		writeJSON(w, http.StatusNotFound, map[string]string{"status": "gone"})
		return
	case status != "active":
		writeJSON(w, http.StatusOK, map[string]any{"status": status, "expires": s.Expires})
		return
	case !srv.isUnlocked(r, "share", s.ID, s.PasswordHash):
		writeJSON(w, http.StatusOK, map[string]any{"status": status, "locked": true, "name": s.Name})
		return
	}
	type file struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		Modified  int64  `json:"modified"`
		NoPreview bool   `json:"noPreview,omitempty"` // sent as a download whatever its kind
	}
	files := []file{}
	for _, it := range srv.shareFiles(&s) {
		files = append(files, file{it.ID, it.Name, it.Size, it.Modified, srv.downloadOnly(&it)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": status, "name": s.Name, "message": s.Message, "expires": s.Expires, "files": files, "password": s.PasswordHash != "",
		"limited": s.MaxDownloads > 0, // every file viewed counts as a download, so the page shows no previews
	})
}

func (srv *server) handleShareUnlock(w http.ResponseWriter, r *http.Request) {
	s, status := srv.shareFor(r)
	if status != "active" {
		jsonError(w, http.StatusNotFound, "This link doesn't work.")
		return
	}
	srv.unlock(w, r, "share", s.ID, s.PasswordHash)
}

// shareDownload checks that a share may be downloaded from, in plain text for terminals. A share
// that reached its download limit gets through, so countDownload can let a resume finish.
func (srv *server) shareDownload(w http.ResponseWriter, r *http.Request) (Share, bool) {
	s, status := srv.shareFor(r)
	if status != "full" && shareClosed(w, status) {
		return s, false
	}
	return s, srv.passwordOK(w, r, "share", s.ID, s.PasswordHash)
}

// shareClosed answers in plain text when a share link can't be used anymore, and says so.
func shareClosed(w http.ResponseWriter, status string) bool {
	switch status {
	case "gone":
		textResponse(w, http.StatusNotFound, "This share link doesn't work. Ask the person who sent it for a new one.\n")
	case "expired":
		textResponse(w, http.StatusGone, "This share link has expired.\n")
	case "full":
		textResponse(w, http.StatusGone, "These files can't be downloaded anymore: the download limit was reached.\n")
	default:
		return false
	}
	return true
}

const (
	downloaderCookie = "stash_downloader"
	resumeWindow     = 24 * time.Hour
)

// resumeKey names one browser's download of one file of a share, or of its zip ("all.zip").
func resumeKey(shareID, file, downloader string) string {
	return shareID + "\x00" + file + "\x00" + downloader
}

// countDownload adds one to the share's downloads, and says whether the download may go ahead.
// Every request for a file's contents counts, with or without ?download, so leaving that off
// doesn't get around the limit. HEAD is free, because it sends no contents. Resuming is free too,
// but only for a browser that was counted for this file in the last day, even if the limit was
// reached since. A Range header on its own proves nothing: anyone can send one. Browsers are told
// apart by a random ID in a cookie, handed out with their first counted download.
// The check and the count happen under one lock, so with several downloads at the same moment
// only as many get through as the limit allows.
func (srv *server) countDownload(w http.ResponseWriter, r *http.Request, shareID, file string) bool {
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	s := srv.st.shareByID(shareID)
	status := "gone"
	if s != nil {
		if status = shareStatus(s); status == "revoked" {
			status = "gone"
		}
	}
	if status != "full" && shareClosed(w, status) {
		return false
	}
	downloader := ""
	if c, err := r.Cookie(downloaderCookie); err == nil && len(c.Value) <= 64 {
		downloader = c.Value
	}
	if r.Header.Get("Range") != "" && downloader != "" && time.Now().Before(srv.resumes[resumeKey(shareID, file, downloader)]) {
		return true
	}
	if shareClosed(w, status) { // the download limit was reached
		return false
	}
	if r.Method == http.MethodHead {
		return true
	}
	s.Downloads++
	if err := srv.st.save(); err != nil {
		log.Printf("saving metadata: %v", err)
	}
	if downloader == "" {
		downloader = newID()
	}
	// Lax rather than Strict: the ID proves nothing, and a resume may start from the browser's
	// list of downloads rather than from this site.
	http.SetCookie(w, &http.Cookie{
		Name: downloaderCookie, Value: downloader, Path: "/s/" + r.PathValue("token") + "/",
		MaxAge: int(resumeWindow / time.Second), HttpOnly: true, Secure: srv.isHTTPS(r), SameSite: http.SameSiteLaxMode,
	})
	srv.resumes[resumeKey(shareID, file, downloader)] = time.Now().Add(resumeWindow)
	return true
}

func (srv *server) handleShareFile(w http.ResponseWriter, r *http.Request) {
	s, ok := srv.shareDownload(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	for _, it := range srv.shareFiles(&s) {
		if it.ID == id {
			if !srv.countDownload(w, r, s.ID, it.ID) {
				return
			}
			srv.serveFile(w, r, &it, r.URL.Query().Has("download"))
			return
		}
	}
	textResponse(w, http.StatusNotFound, "That file isn't part of this share.\n")
}

// handleShareZip streams every file of the share as one zip. Files are stored without
// compression: it's fast, and photos, videos and archives barely shrink anyway.
func (srv *server) handleShareZip(w http.ResponseWriter, r *http.Request) {
	s, ok := srv.shareDownload(w, r)
	if !ok {
		return
	}
	files := srv.shareFiles(&s)
	if !srv.countDownload(w, r, s.ID, "all.zip") {
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": slugify(s.Name) + ".zip"}))
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}

	zw := zip.NewWriter(w)
	used := map[string]bool{}
	for _, it := range files {
		name := it.Name
		for n := 2; used[strings.ToLower(name)]; n++ {
			name = fmt.Sprintf("%d-%s", n, it.Name)
		}
		used[strings.ToLower(name)] = true
		f, err := os.Open(srv.st.blobPath(it.ID))
		if err != nil {
			log.Printf("zip %s: %v", it.ID, err)
			continue
		}
		hdr := &zip.FileHeader{Name: name, Method: zip.Store, Modified: time.UnixMilli(it.Modified), UncompressedSize64: uint64(it.Size)}
		hdr.SetMode(0o644)
		dst, err := zw.CreateHeader(hdr)
		if err == nil {
			_, err = io.Copy(dst, f)
		}
		f.Close()
		if err != nil {
			return // the download was cancelled
		}
	}
	zw.Close()
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if slug == "" {
		return "files"
	}
	return slug
}

// shellQuote makes a file name safe to paste into a bash or zsh command. Inside single quotes
// nothing is expanded, so a name like $(something) stays a name. Control characters can't
// occur: cleanName took them out.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
