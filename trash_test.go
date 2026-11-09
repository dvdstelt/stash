package main

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func (e *testEnv) trash(ids ...string) int {
	e.t.Helper()
	var out struct{ Trashed int }
	e.expect(e.api(http.MethodPost, "/api/items/trash", map[string]any{"ids": ids}), http.StatusOK).decode(e.t, &out)
	return out.Trashed
}

func (e *testEnv) restore(ids ...string) reply {
	e.t.Helper()
	return e.api(http.MethodPost, "/api/items/restore", map[string]any{"ids": ids})
}

func (e *testEnv) deleteForGood(ids ...string) int {
	e.t.Helper()
	var out struct{ Deleted int }
	e.expect(e.api(http.MethodPost, "/api/items/delete", map[string]any{"ids": ids}), http.StatusOK).decode(e.t, &out)
	return out.Deleted
}

func (e *testEnv) folder(parent, name string) Item {
	e.t.Helper()
	var f Item
	e.expect(e.api(http.MethodPost, "/api/folders", map[string]string{"parent": parent, "name": name}), http.StatusCreated).decode(e.t, &f)
	return f
}

// item returns a file or folder as the owner's pages see it, or nil when it's gone.
func (e *testEnv) item(id string) *Item {
	e.t.Helper()
	for _, it := range e.state().Items {
		if it.ID == id {
			return &it
		}
	}
	return nil
}

func TestDeleteMovesToTheTrash(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("aaa"))
	b := e.upload(rootID, "b.txt", []byte("bbb"))
	old := e.folder(rootID, "Old")
	inner := e.upload(old.ID, "inner.txt", []byte("iii"))
	s := e.createShare(nil, a, b, inner)

	if n := e.trash(a.ID, old.ID, "nope"); n != 2 {
		t.Errorf("trashed %d; want 2", n)
	}
	if n := e.trash(a.ID, inner.ID); n != 0 {
		t.Errorf("trashed %d that were in the trash already", n)
	}
	if it := e.item(a.ID); it == nil || it.Trashed == 0 {
		t.Errorf("the file in the trash is %+v", it)
	}
	if got := e.blob(inner.ID); got != "iii" {
		t.Errorf("a file in a deleted folder holds %q; want it kept until it's deleted for good", got)
	}
	// The owner can still look at it, from the trash.
	e.expect(e.send(e.owner, http.MethodGet, "/api/files/"+a.ID+"/content", nil), http.StatusOK)

	// Share links leave out what's in the trash, but keep it, so it's back once it's restored.
	r := e.expect(e.get("/s/"+s.Token), http.StatusOK)
	wantBody(t, r, "b.txt")
	wantNoBody(t, r, "a.txt", "inner.txt")
	if got := readZip(t, e.expect(e.get("/s/"+s.Token+"/all.zip"), http.StatusOK).body); len(got) != 1 || got["b.txt"] != "bbb" {
		t.Errorf("zip = %v", got)
	}
	e.expect(e.get(e.fileURL(s, a)+"?download"), http.StatusNotFound)
	e.expect(e.get(e.fileURL(s, inner)), http.StatusNotFound)
	if files := e.share(s.ID).Files; len(files) != 3 {
		t.Errorf("share files = %v; want all three kept", files)
	}

	r = e.expect(e.api(http.MethodPost, "/api/shares", map[string]any{"name": "x", "files": []string{inner.ID}}), http.StatusBadRequest)
	wantBody(t, r, "One of the files is in the trash. Restore it first.")
	r = e.expect(e.api(http.MethodPost, "/api/uploads", map[string]any{"parent": old.ID, "name": "new.txt", "size": 1}), http.StatusNotFound)
	wantBody(t, r, "That folder is in the trash. Restore it first.")
	for _, id := range []string{rootID, requestsID} {
		r = e.expect(e.api(http.MethodPost, "/api/items/trash", map[string]any{"ids": []string{b.ID, id}}), http.StatusForbidden)
		wantBody(t, r, "Stash needs this folder, so it can't be deleted.")
	}
	if it := e.item(b.ID); it.Trashed != 0 {
		t.Error("a refused delete still moved a file to the trash")
	}

	// Restoring brings it back where it was. A name used again meanwhile gets a number.
	e.upload(rootID, "a.txt", []byte("new"))
	var out struct{ Restored int }
	e.expect(e.restore(a.ID, old.ID, a.ID, b.ID), http.StatusOK).decode(t, &out)
	if out.Restored != 2 {
		t.Errorf("restored %d; want 2", out.Restored)
	}
	if it := e.item(a.ID); it.Trashed != 0 || it.Name != "a (2).txt" || it.Parent != rootID {
		t.Errorf("restored file = %+v", it)
	}
	wantBody(t, e.get("/s/"+s.Token), "a (2).txt", "inner.txt")
	e.expect(e.get(e.fileURL(s, inner)), http.StatusOK)

	st, err := openStore(e.srv.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.items[a.ID].Trashed != 0 || st.items[old.ID].Trashed != 0 {
		t.Error("after a restart, the restored items are in the trash again")
	}
}

// A file deleted before its folder stays in the trash when the folder comes back.
func TestRestoreFromATrashedFolder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	f := e.folder(rootID, "Taxes")
	early := e.upload(f.ID, "draft.txt", []byte("d"))
	e.upload(f.ID, "final.txt", []byte("f"))
	e.trash(early.ID)
	e.trash(f.ID)

	r := e.expect(e.restore(early.ID, f.ID), http.StatusConflict)
	wantBody(t, r, "draft.txt is in a folder that's in the trash too. Restore that folder first.")
	if it := e.item(f.ID); it.Trashed == 0 {
		t.Error("a refused restore still restored the folder")
	}

	e.expect(e.restore(f.ID), http.StatusOK)
	if it := e.item(early.ID); it.Trashed == 0 {
		t.Error("the file deleted on its own came back with its folder")
	}
	e.expect(e.restore(early.ID), http.StatusOK)
	if it := e.item(early.ID); it.Trashed != 0 {
		t.Errorf("the file didn't come back: %+v", it)
	}
}

func TestDeleteForGood(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.upload(rootID, "a.txt", []byte("aaa"))
	b := e.upload(rootID, "b.txt", []byte("bbb"))
	old := e.folder(rootID, "Old")
	inner := e.upload(old.ID, "inner.txt", []byte("iii"))
	s := e.createShare(nil, a, b, inner)

	// Only what's in the trash can be deleted for good, so one click never loses anything.
	if n := e.deleteForGood(a.ID, old.ID); n != 0 {
		t.Errorf("deleted %d that weren't in the trash", n)
	}
	e.trash(a.ID, old.ID)
	if n := e.deleteForGood(a.ID, old.ID, inner.ID, rootID, "nope"); n != 2 {
		t.Errorf("deleted %d; want 2", n)
	}
	for _, f := range []Item{a, inner} {
		if _, err := os.Stat(e.srv.st.blobPath(f.ID)); !os.IsNotExist(err) {
			t.Errorf("the contents of %s are still on disk: %v", f.Name, err)
		}
		e.expect(e.send(e.owner, http.MethodGet, "/api/files/"+f.ID+"/content", nil), http.StatusNotFound)
	}
	if e.item(a.ID) != nil || e.item(old.ID) != nil || e.item(inner.ID) != nil || e.item(b.ID) == nil {
		t.Errorf("items after deleting: %v", e.state().Items)
	}
	if files := e.share(s.ID).Files; len(files) != 1 || files[0] != b.ID {
		t.Errorf("share files = %v; want only %s", files, b.ID)
	}
	e.expect(e.restore(a.ID), http.StatusOK)
	if e.item(a.ID) != nil {
		t.Error("a file deleted for good came back")
	}

	e.trash(b.ID)
	var out struct{ Deleted int }
	e.expect(e.api(http.MethodPost, "/api/trash/empty", nil), http.StatusOK).decode(t, &out)
	if out.Deleted != 1 || e.item(b.ID) != nil {
		t.Errorf("emptying the trash deleted %d, and b.txt is %+v", out.Deleted, e.item(b.ID))
	}
	if files := e.share(s.ID).Files; len(files) != 0 {
		t.Errorf("share files = %v; want none", files)
	}
}

func TestDeletingALinksFolder(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(nil)
	e.expect(e.put(l.Token, "first.txt", strings.NewReader("1")), http.StatusOK)

	// Nobody can upload into the trash.
	e.trash(l.Folder)
	if got := e.state().Links[0]; !got.Revoked {
		t.Error("the link still works while its folder is in the trash")
	}
	r := e.expect(e.put(l.Token, "second.txt", strings.NewReader("2")), http.StatusNotFound)
	wantBody(t, r, "This upload link doesn't work.")
	r = e.expect(e.api(http.MethodPatch, "/api/links/"+l.ID, map[string]any{"revoked": false}), http.StatusConflict)
	wantBody(t, r, "This link's folder is in the trash. Restore the folder first, then reopen the link.")

	// Restoring the folder leaves the link to the owner to reopen.
	e.expect(e.restore(l.Folder), http.StatusOK)
	if got := e.state().Links[0]; !got.Revoked {
		t.Error("restoring the folder reopened the link")
	}
	e.expect(e.api(http.MethodPatch, "/api/links/"+l.ID, map[string]any{"revoked": false}), http.StatusOK)
	e.expect(e.put(l.Token, "second.txt", strings.NewReader("2")), http.StatusOK)

	// Deleting the folder for good removes the link too.
	e.trash(l.Folder)
	e.deleteForGood(l.Folder)
	if links := e.state().Links; len(links) != 0 {
		t.Errorf("links = %+v; want the link gone with its folder", links)
	}
	e.expect(e.put(l.Token, "third.txt", strings.NewReader("3")), http.StatusNotFound)
}

// A stranger's curl -T can still be running when the owner deletes the link's folder for good.
// The file is refused rather than landing among the owner's own files.
func TestUploadWhileTheLinksFolderIsDeletedForGood(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(nil)
	body, send := io.Pipe()
	replies := make(chan reply, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPut, e.url+"/d/"+l.Token+"/late.txt", body)
		req.ContentLength = 8
		resp, err := newTerminal().Do(req)
		if err != nil {
			replies <- reply{body: err.Error()}
			return
		}
		defer resp.Body.Close()
		text, _ := io.ReadAll(resp.Body)
		replies <- reply{status: resp.StatusCode, body: string(text)}
	}()
	if _, err := send.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		e.srv.st.mu.Lock()
		started := len(e.srv.uploads) == 1
		e.srv.st.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the upload didn't start")
		}
	}

	e.trash(l.Folder)
	e.deleteForGood(l.Folder)
	send.Write([]byte("5678"))
	send.Close()
	r := <-replies
	if r.status != http.StatusGone || !strings.Contains(r.body, "This upload link doesn't work anymore.") {
		t.Errorf("got %d %q; want 410 with an explanation", r.status, r.body)
	}
	if _, ok := e.filesIn(rootID)["late.txt"]; ok {
		t.Error("the stranger's file landed in the owner's files")
	}
	e.checkNothingReserved()
}

// Not parallel: it captures the log.
func TestOldTrashIsDeletedForGood(t *testing.T) {
	e := newEnvWith(t, config{TrashDays: 30})
	old := e.upload(rootID, "old.txt", []byte("old"))
	recent := e.upload(rootID, "recent.txt", []byte("new"))
	l := e.createLink(nil)
	e.trash(old.ID, recent.ID, l.Folder)
	e.srv.st.mu.Lock()
	longAgo := time.Now().AddDate(0, 0, -31).UnixMilli()
	e.srv.st.items[old.ID].Trashed = longAgo
	e.srv.st.items[l.Folder].Trashed = longAgo
	e.srv.st.mu.Unlock()

	buf := captureLog(t)
	e.srv.emptyOldTrash()
	if got := logLines(t, buf); len(got) != 1 || got[0] != "Deleted 2 items for good: they were in the trash for 30 days." {
		t.Errorf("logged %q", got)
	}
	st := e.state()
	if st.TrashDays != 30 {
		t.Errorf("the pages are told %d days", st.TrashDays)
	}
	if e.item(old.ID) != nil || e.item(l.Folder) != nil || len(st.Links) != 0 {
		t.Error("what was in the trash for 31 days is still there")
	}
	if it := e.item(recent.ID); it == nil || it.Trashed == 0 {
		t.Errorf("the file deleted just now is %+v; want it still in the trash", it)
	}
	if _, err := os.Stat(e.srv.st.blobPath(old.ID)); !os.IsNotExist(err) {
		t.Errorf("the old file's contents are still on disk: %v", err)
	}
	reopened, err := openStore(e.srv.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.items[old.ID] != nil {
		t.Error("after a restart the old file is back")
	}

	buf.Reset()
	e.srv.emptyOldTrash()
	if got := logLines(t, buf); len(got) != 0 {
		t.Errorf("logged %q with nothing to delete", got)
	}
}
