package main

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type ownerState struct {
	Items   []Item
	Links   []linkView
	Shares  []shareView
	Storage struct{ Used, Free int64 }
	Origin  string
}

func (e *testEnv) state() ownerState {
	e.t.Helper()
	var s ownerState
	e.expect(e.send(e.owner, http.MethodGet, "/api/state", nil), http.StatusOK).decode(e.t, &s)
	return s
}

func TestStateShowsEverything(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := e.upload(rootID, "hello.txt", []byte("hello"))
	l := e.createLink(map[string]any{"name": "Holiday"})
	s := e.createShare(nil, f)

	st := e.state()
	if st.Origin != e.url {
		t.Errorf("origin = %q; want %q", st.Origin, e.url)
	}
	if st.Storage.Used != 5 {
		t.Errorf("storage used = %d; want 5", st.Storage.Used)
	}
	if st.Storage.Free <= 0 {
		t.Errorf("storage free = %d", st.Storage.Free)
	}
	names := map[string]bool{}
	for _, it := range st.Items {
		names[it.Name] = true
	}
	for _, want := range []string{"My files", "Requests", "hello.txt", "Holiday"} {
		if !names[want] {
			t.Errorf("state has no item %q", want)
		}
	}
	if len(st.Links) != 1 || st.Links[0].ID != l.ID || len(st.Shares) != 1 || st.Shares[0].ID != s.ID {
		t.Errorf("links %+v, shares %+v", st.Links, st.Shares)
	}
}

func TestStateUsesThePublicURL(t *testing.T) {
	t.Parallel()
	e := newEnvWith(t, config{PublicURL: "https://files.example.com"})
	if got := e.state().Origin; got != "https://files.example.com" {
		t.Errorf("origin = %q", got)
	}
}

// The owner's pages get to know whether a link has a password, never its hash.
func TestStateHidesPasswordHashes(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := e.upload(rootID, "a.txt", []byte("a"))
	l := e.createLink(map[string]any{"password": "link password"})
	s := e.createShare(map[string]any{"password": "share password"}, f)
	if !l.Password || !s.Password {
		t.Errorf("link password %v, share password %v; want both true", l.Password, s.Password)
	}
	r := e.expect(e.send(e.owner, http.MethodGet, "/api/state", nil), http.StatusOK)
	wantNoBody(t, r, "pbkdf2", "passwordHash", "link password", "share password")
	if r.header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q; want no-store", r.header.Get("Cache-Control"))
	}
}

func TestCreateFolder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	var photos Item
	e.expect(e.api(http.MethodPost, "/api/folders", map[string]string{"parent": rootID, "name": " Photos "}), http.StatusCreated).decode(t, &photos)
	if photos.Type != "folder" || photos.Parent != rootID || photos.Name != "Photos" {
		t.Errorf("folder = %+v", photos)
	}
	var again Item
	e.expect(e.api(http.MethodPost, "/api/folders", map[string]string{"parent": rootID, "name": "photos"}), http.StatusCreated).decode(t, &again)
	if again.Name != "photos (2)" {
		t.Errorf("second folder is called %q; want \"photos (2)\"", again.Name)
	}
	var inner Item
	e.expect(e.api(http.MethodPost, "/api/folders", map[string]string{"parent": photos.ID, "name": "../2025"}), http.StatusCreated).decode(t, &inner)
	if inner.Parent != photos.ID || inner.Name != "2025" {
		t.Errorf("inner folder = %+v", inner)
	}

	r := e.expect(e.api(http.MethodPost, "/api/folders", map[string]string{"parent": rootID, "name": "  "}), http.StatusBadRequest)
	wantBody(t, r, "needs a name")
	e.expect(e.api(http.MethodPost, "/api/folders", map[string]string{"parent": "nope", "name": "x"}), http.StatusNotFound)
	f := e.upload(rootID, "file.txt", []byte("x"))
	e.expect(e.api(http.MethodPost, "/api/folders", map[string]string{"parent": f.ID, "name": "x"}), http.StatusNotFound)

	// It's saved, not only kept in memory.
	st, err := openStore(e.srv.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.items[inner.ID]; got == nil || got.Name != "2025" {
		t.Errorf("after a restart the folder is %+v", got)
	}
}

func TestDeleteFiles(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("aaa"))
	b := e.upload(rootID, "b.txt", []byte("bbb"))
	var folder Item
	e.expect(e.api(http.MethodPost, "/api/folders", map[string]string{"parent": rootID, "name": "Keep"}), http.StatusCreated).decode(t, &folder)
	s := e.createShare(nil, a, b)

	var out struct{ Deleted int }
	e.expect(e.api(http.MethodPost, "/api/items/delete", map[string]any{"ids": []string{a.ID, folder.ID, rootID, "nope"}}), http.StatusOK).decode(t, &out)
	if out.Deleted != 1 {
		t.Errorf("deleted %d; want 1 (folders stay)", out.Deleted)
	}
	if _, err := os.Stat(e.srv.st.blobPath(a.ID)); !os.IsNotExist(err) {
		t.Errorf("the deleted file's contents are still on disk: %v", err)
	}
	names := map[string]bool{}
	for _, it := range e.state().Items {
		names[it.ID] = true
	}
	if names[a.ID] || !names[b.ID] || !names[folder.ID] || !names[rootID] {
		t.Errorf("items after deleting: %v", names)
	}
	if files := e.share(s.ID).Files; len(files) != 1 || files[0] != b.ID {
		t.Errorf("share files = %v; want only %s", files, b.ID)
	}
	e.expect(e.get("/s/"+s.Token+"/files/"+a.ID), http.StatusNotFound)
	e.expect(e.send(e.owner, http.MethodGet, "/api/files/"+a.ID+"/content", nil), http.StatusNotFound)
}

func TestOwnerFileContent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := e.upload(rootID, "notes.txt", []byte("some notes"))

	r := e.expect(e.send(e.owner, http.MethodGet, "/api/files/"+f.ID+"/content", nil), http.StatusOK)
	if r.body != "some notes" || r.header.Get("Content-Type") != "text/plain; charset=utf-8" || !strings.HasPrefix(r.header.Get("Content-Disposition"), "inline") {
		t.Errorf("got %q as %q, %q", r.body, r.header.Get("Content-Type"), r.header.Get("Content-Disposition"))
	}
	r = e.expect(e.send(e.owner, http.MethodGet, "/api/files/"+f.ID+"/content?download", nil), http.StatusOK)
	if r.header.Get("Content-Disposition") != `attachment; filename=notes.txt` {
		t.Errorf("Content-Disposition = %q", r.header.Get("Content-Disposition"))
	}
	r = e.expect(e.send(e.owner, http.MethodGet, "/api/files/"+f.ID+"/content", nil, "Range", "bytes=5-"), http.StatusPartialContent)
	if r.body != "notes" {
		t.Errorf("range = %q; want \"notes\"", r.body)
	}
	e.expect(e.send(e.owner, http.MethodGet, "/api/files/"+rootID+"/content", nil), http.StatusNotFound)
	e.expect(e.send(e.owner, http.MethodGet, "/api/files/nope/content", nil), http.StatusNotFound)

	// The record is there but the contents are gone, say after restoring an old backup.
	if err := os.Remove(e.srv.st.blobPath(f.ID)); err != nil {
		t.Fatal(err)
	}
	r = e.expect(e.send(e.owner, http.MethodGet, "/api/files/"+f.ID+"/content", nil), http.StatusNotFound)
	wantBody(t, r, "missing")
}

func TestRequestBodiesAreChecked(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tests := []struct {
		name   string
		body   string
		status int
		msg    string
	}{
		{"not JSON", "name=x", http.StatusBadRequest, "isn't valid JSON"},
		{"unknown field", `{"name":"x","quota":1,"admin":true}`, http.StatusBadRequest, "isn't valid JSON"},
		{"wrong type", `{"name":"x","quota":"lots"}`, http.StatusBadRequest, "isn't valid JSON"},
		{"two objects", `{"name":"x","quota":1}{"name":"y"}`, http.StatusBadRequest, "extra data"},
		// Only just over 1 MB, so the server reads what's left and the reply arrives in one piece.
		{"too large", `{"name":"` + strings.Repeat("x", 1<<20) + `"}`, http.StatusRequestEntityTooLarge, "too large"},
	}
	for _, tt := range tests {
		r := e.send(e.owner, http.MethodPost, "/api/links", strings.NewReader(tt.body), "X-Stash", "1", "Content-Type", "application/json")
		if r.status != tt.status || !strings.Contains(r.body, tt.msg) {
			t.Errorf("%s: %d %s; want %d with %q", tt.name, r.status, r.body, tt.status, tt.msg)
		}
	}
	if n := len(e.state().Links); n != 0 {
		t.Errorf("%d links were created from bad requests", n)
	}
}

func TestCreateUploadLink(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"name": "  Tax papers  ", "quota": 5_000_000, "maxFiles": 3, "expiresDays": 7, "message": "  Thanks!  "})
	if l.Name != "Tax papers" || l.Quota != 5_000_000 || l.MaxFiles != 3 || l.Message != "Thanks!" || l.Password || l.Revoked {
		t.Errorf("link = %+v", l)
	}
	if len(l.Token) < 20 || l.Token == l.ID {
		t.Errorf("token %q isn't a separate random value", l.Token)
	}
	if want := time.Now().Add(7 * 24 * time.Hour).UnixMilli(); l.Expires < want-60_000 || l.Expires > want+60_000 {
		t.Errorf("expires %d; want about %d", l.Expires, want)
	}
	e.srv.st.mu.Lock()
	folder := e.srv.st.items[l.Folder]
	e.srv.st.mu.Unlock()
	if folder == nil || folder.Parent != requestsID || folder.Name != "Tax papers" {
		t.Errorf("the link's folder is %+v; want \"Tax papers\" in Requests", folder)
	}

	// Two links with the same name each get their own folder.
	other := e.createLink(map[string]any{"name": "Tax papers"})
	if other.Folder == l.Folder {
		t.Error("two links share a folder")
	}
	if never := e.createLink(map[string]any{"expiresDays": 0}); never.Expires != 0 {
		t.Errorf("expiresDays 0 gave expires %d; want never (0)", never.Expires)
	}
}

func TestUploadLinkValidation(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	tests := []struct {
		name   string
		fields map[string]any
		msg    string
	}{
		{"no size limit", map[string]any{"name": "x"}, "Choose a size limit"},
		{"no name", map[string]any{"quota": 10}, "Give the link a name"},
		{"blank name", map[string]any{"name": "   ", "quota": 10}, "at most 120"},
		{"long name", map[string]any{"name": strings.Repeat("n", 121), "quota": 10}, "at most 120"},
		{"zero quota", map[string]any{"name": "x", "quota": 0}, "more than 0"},
		{"negative quota", map[string]any{"name": "x", "quota": -1}, "more than 0"},
		{"huge quota", map[string]any{"name": "x", "quota": int64(1)<<60 + 1}, "more than 0"},
		{"negative max files", map[string]any{"name": "x", "quota": 10, "maxFiles": -1}, "number of files"},
		{"too many days", map[string]any{"name": "x", "quota": 10, "expiresDays": 3651}, "10 years"},
		{"negative days", map[string]any{"name": "x", "quota": 10, "expiresDays": -1}, "10 years"},
		{"long message", map[string]any{"name": "x", "quota": 10, "message": strings.Repeat("m", 1001)}, "1000 characters"},
		{"long password", map[string]any{"name": "x", "quota": 10, "password": strings.Repeat("p", 201)}, "too long"},
	}
	for _, tt := range tests {
		r := e.api(http.MethodPost, "/api/links", tt.fields)
		if r.status != http.StatusBadRequest || !strings.Contains(r.body, tt.msg) {
			t.Errorf("%s: %d %s; want 400 with %q", tt.name, r.status, r.body, tt.msg)
		}
	}
	// 120 characters is fine, counted as characters rather than bytes.
	e.createLink(map[string]any{"name": strings.Repeat("é", 120)})
	if n := len(e.state().Links); n != 1 {
		t.Errorf("%d links exist; want 1", n)
	}
}

func TestUpdateUploadLink(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(map[string]any{"name": "Before", "quota": 100, "maxFiles": 2, "expiresDays": 3, "message": "hi"})

	var got linkView
	e.expect(e.api(http.MethodPatch, "/api/links/"+l.ID, map[string]any{"name": "After"}), http.StatusOK).decode(t, &got)
	if got.Name != "After" || got.Quota != 100 || got.MaxFiles != 2 || got.Expires != l.Expires || got.Message != "hi" {
		t.Errorf("changing the name changed more: %+v", got)
	}
	e.srv.st.mu.Lock()
	folderName := e.srv.st.items[l.Folder].Name
	e.srv.st.mu.Unlock()
	if folderName != "After" {
		t.Errorf("the link's folder is still called %q", folderName)
	}

	e.expect(e.api(http.MethodPatch, "/api/links/"+l.ID, map[string]any{"quota": 500, "maxFiles": 0, "expiresDays": 0, "message": ""}), http.StatusOK).decode(t, &got)
	if got.Quota != 500 || got.MaxFiles != 0 || got.Expires != 0 || got.Message != "" {
		t.Errorf("after the change: %+v", got)
	}

	e.changeLink(l.ID, func(l *Link) { l.PasswordHash = fastHash("secret") })
	e.expect(e.api(http.MethodPatch, "/api/links/"+l.ID, map[string]any{"password": ""}), http.StatusOK).decode(t, &got)
	if got.Password {
		t.Error("an empty password doesn't remove the password")
	}

	e.expect(e.api(http.MethodPatch, "/api/links/"+l.ID, map[string]any{"revoked": true}), http.StatusOK).decode(t, &got)
	if !got.Revoked {
		t.Error("the link isn't revoked")
	}
	e.expect(e.api(http.MethodPatch, "/api/links/"+l.ID, map[string]any{"quota": 0}), http.StatusBadRequest)
	e.expect(e.api(http.MethodPatch, "/api/links/nope", map[string]any{"name": "x"}), http.StatusNotFound)
}

func TestCreateShareLink(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("a"))
	b := e.upload(rootID, "b.txt", []byte("b"))

	var s shareView
	e.expect(e.api(http.MethodPost, "/api/shares", map[string]any{
		"name": "Two files", "files": []string{a.ID, b.ID, a.ID}, "maxDownloads": 3, "expiresDays": 1, "message": "enjoy",
	}), http.StatusCreated).decode(t, &s)
	if s.Name != "Two files" || len(s.Files) != 2 || s.Files[0] != a.ID || s.Files[1] != b.ID {
		t.Errorf("share = %+v; want both files once, in order", s)
	}
	if s.MaxDownloads != 3 || s.Downloads != 0 || s.Expires == 0 || s.Message != "enjoy" || s.Password || s.Revoked {
		t.Errorf("share = %+v", s)
	}

	var folder Item
	e.expect(e.api(http.MethodPost, "/api/folders", map[string]string{"parent": rootID, "name": "F"}), http.StatusCreated).decode(t, &folder)
	tests := []struct {
		name   string
		fields map[string]any
		msg    string
	}{
		{"no files", map[string]any{"name": "x"}, "at least one file"},
		{"empty files", map[string]any{"name": "x", "files": []string{}}, "at least one file"},
		{"unknown file", map[string]any{"name": "x", "files": []string{a.ID, "nope"}}, "doesn't exist"},
		{"a folder", map[string]any{"name": "x", "files": []string{folder.ID}}, "doesn't exist"},
		{"no name", map[string]any{"files": []string{a.ID}}, "Give the link a name"},
		{"negative limit", map[string]any{"name": "x", "files": []string{a.ID}, "maxDownloads": -1}, "number of downloads"},
	}
	for _, tt := range tests {
		r := e.api(http.MethodPost, "/api/shares", tt.fields)
		if r.status != http.StatusBadRequest || !strings.Contains(r.body, tt.msg) {
			t.Errorf("%s: %d %s; want 400 with %q", tt.name, r.status, r.body, tt.msg)
		}
	}
	if n := len(e.state().Shares); n != 1 {
		t.Errorf("%d shares exist; want 1", n)
	}
}

func TestUpdateShareLink(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("a"))
	b := e.upload(rootID, "b.txt", []byte("b"))
	s := e.createShare(map[string]any{"maxDownloads": 1}, a)

	r := e.expect(e.api(http.MethodPatch, "/api/shares/"+s.ID, map[string]any{"files": []string{b.ID}}), http.StatusBadRequest)
	wantBody(t, r, "can't be changed")

	var got shareView
	e.expect(e.api(http.MethodPatch, "/api/shares/"+s.ID, map[string]any{"name": "Renamed", "maxDownloads": 5, "expiresDays": 2}), http.StatusOK).decode(t, &got)
	if got.Name != "Renamed" || got.MaxDownloads != 5 || got.Expires == 0 || len(got.Files) != 1 || got.Files[0] != a.ID {
		t.Errorf("share = %+v", got)
	}
	e.expect(e.api(http.MethodPatch, "/api/shares/"+s.ID, map[string]any{"revoked": true}), http.StatusOK).decode(t, &got)
	if !got.Revoked {
		t.Error("the share isn't revoked")
	}
	e.expect(e.get("/s/"+s.Token+"/files/"+a.ID), http.StatusNotFound)
	e.expect(e.api(http.MethodPatch, "/api/shares/"+s.ID, map[string]any{"revoked": false}), http.StatusOK)
	e.expect(e.get("/s/"+s.Token+"/files/"+a.ID), http.StatusOK)
	e.expect(e.api(http.MethodPatch, "/api/shares/nope", map[string]any{"name": "x"}), http.StatusNotFound)
}
