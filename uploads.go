package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// An upload in progress. Browsers send files in chunks so a dropped connection only costs the
// current chunk; the command line sends one long request.
type upload struct {
	mu       sync.Mutex // one chunk at a time
	id       string
	name     string
	parent   string
	path     []string // folders inside parent to put the file in, made when it's done
	linkID   string   // set for uploads through an upload link
	size     int64    // expected size, or -1 if the sender didn't say (console upload without Content-Length)
	reserved int64    // bytes set aside for this upload
	received int64
	file     *os.File
	touched  atomic.Int64 // when the last bytes arrived, in Unix nanoseconds; read without mu
	done     bool
}

func (u *upload) touch()                { u.touched.Store(time.Now().UnixNano()) }
func (u *upload) lastActive() time.Time { return time.Unix(0, u.touched.Load()) }

// httpError carries a status code and a message meant for the person uploading.
type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func errorStatus(err error) (int, string) {
	var he *httpError
	if errors.As(err, &he) {
		return he.status, he.msg
	}
	log.Printf("upload error: %v", err)
	return http.StatusInternalServerError, "Something went wrong on the server."
}

const (
	diskReserve = 512 << 20 // never fill the disk completely

	// Every upload in progress holds an open file and the space it asked for, so only so many
	// may run at once. Browsers send four files at a time, which leaves room for several people
	// on one link.
	maxUploadsPerLink = 25
	maxUploadsTotal   = 250
)

// spaceLeft is the free disk space minus what uploads in progress may still use. Hold st.mu.
func (srv *server) spaceLeft() int64 {
	return max(0, diskFree(srv.st.dir)-srv.inflight-diskReserve)
}

// startUpload checks the limits and sets space aside. With size -1 it sets aside everything
// that's left, and settles on the real size when the upload ends. path is the folder the file
// was in when a whole folder is uploaded, such as "Trip/Day 1", or "".
func (srv *server) startUpload(name, path, parent string, link *Link, size int64) (*upload, error) {
	name, err := cleanName(name)
	if err != nil {
		return nil, &httpError{http.StatusBadRequest, "The file needs a name."}
	}
	folders, err := cleanPath(path)
	if err != nil {
		return nil, &httpError{http.StatusBadRequest, err.Error()}
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()

	if folder := srv.st.items[parent]; folder == nil || folder.Type != "folder" {
		return nil, &httpError{http.StatusNotFound, "That folder doesn't exist anymore."}
	}
	if srv.st.inTrash(parent) {
		return nil, &httpError{http.StatusNotFound, "That folder is in the trash. Restore it first."}
	}
	if len(srv.uploads) >= maxUploadsTotal || (link != nil && srv.pending[link.ID] >= maxUploadsPerLink) {
		return nil, &httpError{http.StatusTooManyRequests, "Too many uploads are running at the same time. Try again in a moment."}
	}
	limit := srv.spaceLeft()
	tooLarge := fmt.Sprintf("Not enough space on the server: %s is free.", formatBytes(limit))
	if link != nil {
		used, files := srv.st.linkUsage(link)
		if link.MaxFiles > 0 && files+srv.pending[link.ID] >= link.MaxFiles {
			return nil, &httpError{http.StatusForbidden, fmt.Sprintf("This link takes at most %d files.", link.MaxFiles)}
		}
		if left := max(0, link.Quota-used-srv.reserved[link.ID]); left < limit {
			limit = left
			tooLarge = fmt.Sprintf("Too large: only %s is left on this link.", formatBytes(left))
			if left == 0 {
				tooLarge = "This link is full."
			}
		}
	}
	if size > limit || (size < 0 && limit == 0) {
		return nil, &httpError{http.StatusRequestEntityTooLarge, tooLarge}
	}

	u := &upload{id: newID(), name: name, parent: parent, path: folders, size: size, reserved: size}
	u.touch()
	if size < 0 {
		u.reserved = limit
	}
	if u.file, err = os.OpenFile(srv.st.partPath(u.id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
		return nil, err
	}
	srv.uploads[u.id] = u
	srv.inflight += u.reserved
	if link != nil {
		u.linkID = link.ID
		srv.reserved[link.ID] += u.reserved
		srv.pending[link.ID]++
	}
	return u, nil
}

// releaseLocked gives back the space set aside for an upload. Hold st.mu.
func (srv *server) releaseLocked(u *upload) {
	if _, ok := srv.uploads[u.id]; !ok {
		return
	}
	delete(srv.uploads, u.id)
	srv.inflight -= u.reserved
	if u.linkID != "" {
		srv.reserved[u.linkID] -= u.reserved
		if srv.pending[u.linkID]--; srv.pending[u.linkID] <= 0 {
			delete(srv.pending, u.linkID)
			delete(srv.reserved, u.linkID)
		}
	}
}

func (srv *server) cancelUpload(u *upload) {
	u.mu.Lock()
	defer u.mu.Unlock()
	srv.cancelLocked(u)
}

// cancelLocked throws an unfinished upload away. Hold u.mu.
func (srv *server) cancelLocked(u *upload) {
	if u.done {
		return
	}
	u.done = true
	u.file.Close()
	os.Remove(srv.st.partPath(u.id))
	srv.st.mu.Lock()
	srv.releaseLocked(u)
	srv.st.mu.Unlock()
}

type offsetError struct{ received int64 }

func (e *offsetError) Error() string { return "the upload continues at a different offset" }

// receive appends the request body to the upload at offset. When the last byte is in, it turns
// the upload into a file and returns it.
func (srv *server) receive(w http.ResponseWriter, r *http.Request, u *upload, offset int64) (received int64, item *Item, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.done {
		return 0, nil, &httpError{http.StatusNotFound, "This upload doesn't exist anymore."}
	}
	if offset != u.received {
		return u.received, nil, &offsetError{u.received}
	}

	room := u.reserved - u.received
	body := &deadlineReader{r: r.Body, rc: http.NewResponseController(w)}
	buf := make([]byte, 1<<20)
	n, copyErr := io.CopyBuffer(touchWriter{u}, io.LimitReader(body, room+1), buf)
	u.received += n
	u.touch()
	if u.received > u.reserved {
		msg := "The file is larger than it said it would be."
		if u.size < 0 {
			msg = "Too large: there isn't enough space left for this file."
		}
		return u.received, nil, &httpError{http.StatusRequestEntityTooLarge, msg}
	}
	if errors.Is(copyErr, errTooSlow) {
		return u.received, nil, &httpError{http.StatusRequestTimeout, "The upload was too slow and was stopped."}
	}
	if copyErr != nil {
		return u.received, nil, &httpError{http.StatusBadRequest, "The upload was interrupted."}
	}
	if u.received == u.size || u.size < 0 {
		item, err = srv.finishLocked(u)
	}
	return u.received, item, err
}

// finishLocked moves the finished upload into place as a new file. Hold u.mu.
func (srv *server) finishLocked(u *upload) (*Item, error) {
	if err := u.file.Sync(); err != nil {
		return nil, err
	}
	if err := u.file.Close(); err != nil {
		return nil, err
	}
	u.done = true

	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	defer srv.releaseLocked(u)
	parent := srv.st.items[u.parent]
	if parent == nil && u.linkID != "" {
		// The link's folder was deleted for good while the upload ran, and the link with it. A
		// stranger's file doesn't belong among the owner's own files.
		os.Remove(srv.st.partPath(u.id))
		return nil, &httpError{http.StatusGone, "This upload link doesn't work anymore. Ask the person who sent it for a new one."}
	}
	if parent == nil { // the owner deleted the folder for good while the upload ran
		parent = srv.st.items[rootID]
	}
	// The folders are made only now, so an upload that fails or is cancelled leaves no empty
	// folders behind, and a stranger can't make folders without sending files.
	for _, name := range u.path {
		parent = srv.st.subfolder(parent.ID, name)
	}
	now := nowMillis()
	it := &Item{ID: newID(), Type: "file", Parent: parent.ID, Name: srv.st.uniqueName(parent.ID, u.name), Size: u.received, Modified: now}
	if err := os.Rename(srv.st.partPath(u.id), srv.st.blobPath(it.ID)); err != nil {
		os.Remove(srv.st.partPath(u.id))
		return nil, err
	}
	srv.st.items[it.ID] = it
	parent.Modified = now
	if err := srv.st.save(); err != nil {
		log.Printf("saving metadata: %v", err)
	}
	return it, nil
}

// touchWriter writes to the upload's file and notes the time, so the janitor can tell a live
// upload from an abandoned one without waiting for its lock.
type touchWriter struct{ u *upload }

func (t touchWriter) Write(p []byte) (int, error) {
	t.u.touch()
	return t.u.file.Write(p)
}

const (
	uploadIdleTimeout = 2 * time.Minute
	minUploadBytes    = 256 << 10 // per uploadIdleTimeout: about 2 KB/s
)

var errTooSlow = errors.New("the upload is too slow")

// deadlineReader gives up when the sender goes quiet for two minutes, or sends less than 256 KB
// in two minutes, instead of holding the connection and the space set aside for the upload
// forever: a trickle of bytes would otherwise keep both for as long as it liked. Uploads at any
// normal speed may take as long as they need.
type deadlineReader struct {
	r           io.Reader
	rc          *http.ResponseController
	last        time.Time // when the read deadline was last moved
	windowStart time.Time
	windowBytes int64
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	now := time.Now()
	if d.windowStart.IsZero() {
		d.windowStart = now
	}
	if now.Sub(d.last) > 5*time.Second {
		d.rc.SetReadDeadline(now.Add(uploadIdleTimeout))
		d.last = now
	}
	if now.Sub(d.windowStart) >= uploadIdleTimeout {
		if d.windowBytes < minUploadBytes {
			return 0, errTooSlow
		}
		d.windowStart, d.windowBytes = now, 0
	}
	n, err := d.r.Read(p)
	d.windowBytes += int64(n)
	return n, err
}

/* ---------- Chunked uploads from a browser ---------- */

type uploadHandler func(w http.ResponseWriter, r *http.Request, u *upload)

func (srv *server) findUpload(w http.ResponseWriter, r *http.Request, linkID string) *upload {
	srv.st.mu.Lock()
	u := srv.uploads[r.PathValue("id")]
	srv.st.mu.Unlock()
	if u == nil || u.linkID != linkID {
		jsonError(w, http.StatusNotFound, "This upload doesn't exist anymore. Start it again.")
		return nil
	}
	return u
}

func (srv *server) ownerUpload(next uploadHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u := srv.findUpload(w, r, ""); u != nil {
			next(w, r, u)
		}
	}
}

func (srv *server) handleOwnerUploadStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Parent, Name, Path string
		Size               int64
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Size < 0 {
		jsonError(w, http.StatusBadRequest, "The size can't be negative.")
		return
	}
	u, err := srv.startUpload(in.Name, in.Path, in.Parent, nil, in.Size)
	if err != nil {
		status, msg := errorStatus(err)
		jsonError(w, status, msg)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": u.id})
}

func (srv *server) handleUploadStatus(w http.ResponseWriter, r *http.Request, u *upload) {
	u.mu.Lock()
	defer u.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]int64{"received": u.received, "size": u.size})
}

func (srv *server) handleUploadChunk(w http.ResponseWriter, r *http.Request, u *upload) {
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		jsonError(w, http.StatusBadRequest, "Add ?offset= with the position of this chunk.")
		return
	}
	received, item, err := srv.receive(w, r, u, offset)
	var oe *offsetError
	switch {
	case errors.As(err, &oe):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Continue from the right position.", "received": oe.received})
	case err != nil:
		status, msg := errorStatus(err)
		if status != http.StatusBadRequest { // an interrupted chunk can be sent again; anything else can't
			srv.cancelUpload(u)
		}
		writeJSON(w, status, map[string]any{"error": msg, "received": received})
	case item != nil:
		writeJSON(w, http.StatusOK, map[string]any{"done": true, "item": item})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"received": received})
	}
}

func (srv *server) handleUploadCancel(w http.ResponseWriter, r *http.Request, u *upload) {
	srv.cancelUpload(u)
	w.WriteHeader(http.StatusNoContent)
}

/* ---------- Uploads from the command line ---------- */

// handleConsoleUpload takes a plain PUT of a whole file, which is what curl -T, wget --method=PUT
// and PowerShell's Invoke-RestMethod -Method Put send. Replies are plain text for the terminal.
func (srv *server) handleConsoleUpload(w http.ResponseWriter, r *http.Request) {
	link, ok := srv.consoleLink(w, r)
	if !ok {
		return
	}
	u, err := srv.startUpload(r.PathValue("name"), "", link.Folder, link, r.ContentLength)
	if err != nil {
		status, msg := errorStatus(err)
		consoleError(w, r, status, msg+"\n")
		return
	}
	_, item, err := srv.receive(w, r, u, 0)
	if err == nil && item == nil {
		err = &httpError{http.StatusBadRequest, "The upload was interrupted."}
	}
	if err != nil {
		srv.cancelUpload(u)
		status, msg := errorStatus(err)
		if status == http.StatusBadRequest {
			msg += " Run the command again to start over."
		}
		consoleError(w, r, status, msg+"\n")
		return
	}
	saved := item.Name
	if saved != u.name {
		saved = fmt.Sprintf("%s (saved as %s, a file with that name already exists)", u.name, item.Name)
	}
	// 200 rather than 201: wget2 only prints the reply of a 200, and the confirmation matters in a terminal.
	textResponse(w, http.StatusOK, fmt.Sprintf("Uploaded %s, %s.\n", saved, formatBytes(item.Size)))
}

// consoleLink finds the link for a command-line request and checks its password, which comes
// in a Link-Password header.
func (srv *server) consoleLink(w http.ResponseWriter, r *http.Request) (*Link, bool) {
	srv.st.mu.Lock()
	l := srv.st.linkByToken(r.PathValue("token"))
	var link Link
	var status string
	if l != nil {
		link = *l
		used, files := srv.st.linkUsage(l)
		status = linkStatus(l, used, files)
	}
	srv.st.mu.Unlock()

	switch {
	case l == nil || status == "revoked":
		consoleError(w, r, http.StatusNotFound, "This upload link doesn't work. Ask the person who sent it for a new one.\n")
		return nil, false
	case status == "expired":
		consoleError(w, r, http.StatusGone, "This upload link has expired. Ask the person who sent it for a new one.\n")
		return nil, false
	}
	// Like the web page, say nothing more about a password-protected link until the password is right.
	if !srv.passwordOK(w, r, "link", link.ID, link.PasswordHash) {
		return nil, false
	}
	if status == "full" {
		consoleError(w, r, http.StatusRequestEntityTooLarge, "This upload link is full. Ask the person who sent it if you need to upload more.\n")
		return nil, false
	}
	return &link, true
}

// passwordOK checks a Link-Password header (or an unlock cookie) for console requests,
// answering in plain text when it's missing or wrong.
func (srv *server) passwordOK(w http.ResponseWriter, r *http.Request, kind, id, hash string) bool {
	if srv.isUnlocked(r, kind, id, hash) {
		return true
	}
	given := r.Header.Get("Link-Password")
	if given == "" {
		consoleError(w, r, http.StatusUnauthorized, "This link has a password. Send it in a Link-Password header.\n"+
			"  curl:       -H \"Link-Password: YOUR-PASSWORD\"\n"+
			"  PowerShell: -Headers @{ 'Link-Password' = 'YOUR-PASSWORD' }\n")
		return false
	}
	key := kind + ":" + id + "|" + srv.visitor(r)
	if wait := srv.limits.wait(key); wait > 0 {
		consoleError(w, r, http.StatusTooManyRequests, fmt.Sprintf("Too many wrong passwords. Try again in %s.\n", wait.Round(time.Second)))
		return false
	}
	ok, err := checkPassword(hash, given)
	if err != nil {
		status, msg := hashProblem(err, "Couldn't check the password.")
		consoleError(w, r, status, msg+"\n")
		return false
	}
	if !ok {
		srv.wrongPassword(r, key, kind, id)
		consoleError(w, r, http.StatusUnauthorized, "That password isn't right.\n")
		return false
	}
	srv.limits.succeed(key)
	return true
}

// consoleError answers a console request with a plain-text error. PowerShell and wget send the
// whole file before they read the reply, so if the server answers early they only report a broken
// connection. For files up to 64 MB the server reads and discards the rest first, so they show the
// message. curl asks first (Expect: 100-continue) and gets the message without sending anything.
func consoleError(w http.ResponseWriter, r *http.Request, status int, text string) {
	if !strings.EqualFold(r.Header.Get("Expect"), "100-continue") && r.ContentLength > 0 && r.ContentLength <= 64<<20 {
		io.Copy(io.Discard, &deadlineReader{r: io.LimitReader(r.Body, 64<<20), rc: http.NewResponseController(w)})
	}
	textResponse(w, status, text)
}
