package main

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// What the owner's pages see of links: everything except the password hash.
type linkView struct {
	ID       string `json:"id"`
	Token    string `json:"token"`
	Name     string `json:"name"`
	Folder   string `json:"folder"`
	Quota    int64  `json:"quota"`
	MaxFiles int    `json:"maxFiles"`
	Created  int64  `json:"created"`
	Expires  int64  `json:"expires"`
	Password bool   `json:"password"`
	Message  string `json:"message"`
	Revoked  bool   `json:"revoked"`
}

type shareView struct {
	ID           string   `json:"id"`
	Token        string   `json:"token"`
	Name         string   `json:"name"`
	Files        []string `json:"files"`
	Folder       string   `json:"folder"`
	Created      int64    `json:"created"`
	Expires      int64    `json:"expires"`
	MaxDownloads int      `json:"maxDownloads"`
	Downloads    int      `json:"downloads"`
	Password     bool     `json:"password"`
	Message      string   `json:"message"`
	Revoked      bool     `json:"revoked"`
}

func viewLink(l *Link) linkView {
	return linkView{l.ID, l.Token, l.Name, l.Folder, l.Quota, l.MaxFiles, l.Created, l.Expires, l.PasswordHash != "", l.Message, l.Revoked}
}

func viewShare(s *Share) shareView {
	files := s.Files
	if files == nil {
		files = []string{}
	}
	return shareView{s.ID, s.Token, s.Name, files, s.Folder, s.Created, s.Expires, s.MaxDownloads, s.Downloads, s.PasswordHash != "", s.Message, s.Revoked}
}

// handleState returns everything the owner's pages show. Stash is meant for one person's
// files, so sending it all at once keeps the pages simple.
func (srv *server) handleState(w http.ResponseWriter, r *http.Request) {
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	state := struct {
		Items   []*Item     `json:"items"`
		Links   []linkView  `json:"links"`
		Shares  []shareView `json:"shares"`
		Storage struct {
			Used int64 `json:"used"`
			Free int64 `json:"free"`
		} `json:"storage"`
		Origin    string `json:"origin"`
		TrashDays int    `json:"trashDays"`
	}{Items: []*Item{}, Links: []linkView{}, Shares: []shareView{}, Origin: srv.origin(r), TrashDays: srv.cfg.TrashDays}
	for _, it := range srv.st.items {
		state.Items = append(state.Items, it)
	}
	for _, l := range srv.st.links {
		state.Links = append(state.Links, viewLink(l))
	}
	for _, s := range srv.st.shares {
		state.Shares = append(state.Shares, viewShare(s))
	}
	state.Storage.Used = srv.st.usedBytes()
	state.Storage.Free = srv.spaceLeft()
	writeJSON(w, http.StatusOK, state)
}

func (srv *server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	var in struct{ Parent, Name string }
	if !readJSON(w, r, &in) {
		return
	}
	name, err := cleanName(in.Name)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "The folder needs a name.")
		return
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	if parent := srv.st.items[in.Parent]; parent == nil || parent.Type != "folder" {
		jsonError(w, http.StatusNotFound, "That folder doesn't exist.")
		return
	}
	it := srv.st.addFolder(in.Parent, name)
	if !srv.saveOrFail(w) {
		return
	}
	writeJSON(w, http.StatusCreated, it)
}

/* ---------- The trash ---------- */

// handleTrashItems moves files and folders to the trash. They're deleted for good after
// -trash-days, or earlier when the owner empties the trash.
func (srv *server) handleTrashItems(w http.ResponseWriter, r *http.Request) {
	var in struct{ IDs []string }
	if !readJSON(w, r, &in) {
		return
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	if slices.Contains(in.IDs, rootID) || slices.Contains(in.IDs, requestsID) {
		jsonError(w, http.StatusForbidden, "Stash needs this folder, so it can't be deleted.")
		return
	}
	now := nowMillis()
	trashed := 0
	for _, id := range in.IDs {
		if it := srv.st.items[id]; it != nil && !srv.st.inTrash(id) {
			it.Trashed = now
			trashed++
		}
	}
	// Nobody should upload into the trash. Restoring the folder leaves the link revoked, so the
	// owner decides whether to open it again.
	for _, l := range srv.st.links {
		if srv.st.inTrash(l.Folder) {
			l.Revoked = true
		}
	}
	if !srv.saveOrFail(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"trashed": trashed})
}

func (srv *server) handleRestoreItems(w http.ResponseWriter, r *http.Request) {
	var in struct{ IDs []string }
	if !readJSON(w, r, &in) {
		return
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	var items []*Item
	for _, id := range in.IDs {
		it := srv.st.items[id]
		if it == nil || it.Trashed == 0 || slices.Contains(items, it) {
			continue
		}
		// Something deleted before the folder it's in comes back with that folder, not on its own.
		if srv.st.inTrash(it.Parent) {
			jsonError(w, http.StatusConflict, fmt.Sprintf("%s is in a folder that's in the trash too. Restore that folder first.", it.Name))
			return
		}
		items = append(items, it)
	}
	for _, it := range items {
		// Its name may have been used again meanwhile. It's still in the trash here, so it doesn't clash with itself.
		it.Name = srv.st.uniqueName(it.Parent, it.Name)
		it.Trashed = 0
	}
	if !srv.saveOrFail(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"restored": len(items)})
}

// handleDeleteItems deletes items in the trash for good. Anything else has to go to the trash
// first, so nothing is lost with one click.
func (srv *server) handleDeleteItems(w http.ResponseWriter, r *http.Request) {
	var in struct{ IDs []string }
	if !readJSON(w, r, &in) {
		return
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	var items []*Item
	for _, id := range in.IDs {
		if it := srv.st.items[id]; it != nil && it.Trashed != 0 {
			items = append(items, it)
		}
	}
	srv.deleteForGood(w, items)
}

func (srv *server) handleEmptyTrash(w http.ResponseWriter, r *http.Request) {
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	srv.deleteForGood(w, srv.st.trashedSince(0))
}

// deleteForGood removes items and answers with how many it removed. Hold st.mu.
func (srv *server) deleteForGood(w http.ResponseWriter, items []*Item) {
	deleted, err := srv.st.purgeAll(items)
	if err != nil {
		srv.st.save() // what was removed before the error stays removed
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !srv.saveOrFail(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": deleted})
}

// handleRenameItem renames a file or folder. A name that's already taken in the same folder is
// refused rather than changed to "name (2)", so the owner ends up with the name they typed.
func (srv *server) handleRenameItem(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name string }
	if !readJSON(w, r, &in) {
		return
	}
	if strings.ContainsAny(in.Name, `/\`) {
		jsonError(w, http.StatusBadRequest, `Names can't contain / or \.`)
		return
	}
	name, err := cleanName(in.Name)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "Type a name.")
		return
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	it := srv.st.items[r.PathValue("id")]
	switch {
	case it == nil:
		jsonError(w, http.StatusNotFound, "That file or folder doesn't exist anymore.")
		return
	case it.ID == rootID || it.ID == requestsID:
		jsonError(w, http.StatusForbidden, "Stash needs this folder, so it can't be renamed.")
		return
	case srv.st.nameTaken(it.Parent, name, it.ID):
		jsonError(w, http.StatusConflict, fmt.Sprintf("There's already something called %s in this folder.", name))
		return
	}
	// An upload link's folder carries the link's name, and the other way round.
	link := srv.st.linkByFolder(it.ID)
	if link != nil && utf8.RuneCountInString(name) > 120 {
		jsonError(w, http.StatusBadRequest, "This folder belongs to an upload link, so its name can be at most 120 characters.")
		return
	}
	it.Name = name
	if link != nil {
		link.Name = name
	}
	if !srv.saveOrFail(w) {
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (srv *server) handleFileContent(w http.ResponseWriter, r *http.Request) {
	srv.st.mu.Lock()
	it := srv.st.items[r.PathValue("id")]
	var file Item
	if it != nil {
		file = *it
	}
	srv.st.mu.Unlock()
	if it == nil || file.Type != "file" {
		http.NotFound(w, r)
		return
	}
	srv.serveFile(w, r, &file, r.URL.Query().Has("download"))
}

/* ---------- Upload links and share links ---------- */

// linkInput is what the pages send when creating or changing a link. Missing fields stay as they are.
type linkInput struct {
	Name         *string   `json:"name"`
	Quota        *int64    `json:"quota"`
	MaxFiles     *int      `json:"maxFiles"`
	MaxDownloads *int      `json:"maxDownloads"`
	ExpiresDays  *int      `json:"expiresDays"` // 0 means never
	Password     *string   `json:"password"`    // "" removes the password
	Message      *string   `json:"message"`
	Revoked      *bool     `json:"revoked"`
	Files        *[]string `json:"files"`
	Folder       *string   `json:"folder"` // a share link holds files or one folder
}

// validate checks the input and hashes a new password, before any lock is taken.
func (in *linkInput) validate(creating bool) (passwordHash *string, err error) {
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
		if n := utf8.RuneCountInString(*in.Name); n == 0 || n > 120 {
			return nil, fmt.Errorf("Give the link a name of at most 120 characters.")
		}
	} else if creating {
		return nil, fmt.Errorf("Give the link a name.")
	}
	if in.Quota != nil && (*in.Quota <= 0 || *in.Quota > 1<<60) {
		return nil, fmt.Errorf("The size limit has to be more than 0.")
	}
	if in.MaxFiles != nil && (*in.MaxFiles < 0 || *in.MaxFiles > 1_000_000) {
		return nil, fmt.Errorf("The maximum number of files isn't valid.")
	}
	if in.MaxDownloads != nil && (*in.MaxDownloads < 0 || *in.MaxDownloads > 1_000_000) {
		return nil, fmt.Errorf("The maximum number of downloads isn't valid.")
	}
	if in.ExpiresDays != nil && (*in.ExpiresDays < 0 || *in.ExpiresDays > 3650) {
		return nil, fmt.Errorf("Links can expire after at most 10 years.")
	}
	if in.Message != nil {
		*in.Message = strings.TrimSpace(*in.Message)
		if utf8.RuneCountInString(*in.Message) > 1000 {
			return nil, fmt.Errorf("Keep the message under 1000 characters.")
		}
	}
	if in.Password != nil {
		hash := ""
		if *in.Password != "" {
			if len(*in.Password) > 200 {
				return nil, fmt.Errorf("The password is too long.")
			}
			if hash, err = hashPassword(*in.Password); err != nil {
				return nil, err
			}
		}
		passwordHash = &hash
	}
	return passwordHash, nil
}

func expiresAt(days int) int64 {
	if days == 0 {
		return 0
	}
	return time.Now().Add(time.Duration(days) * 24 * time.Hour).UnixMilli()
}

func (srv *server) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	var in linkInput
	if !readJSON(w, r, &in) {
		return
	}
	if in.Quota == nil {
		jsonError(w, http.StatusBadRequest, "Choose a size limit.")
		return
	}
	hash, err := in.validate(true)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	l := &Link{ID: newID(), Token: newID(), Name: *in.Name, Created: nowMillis()}
	l.Folder = srv.st.addFolder(requestsID, l.Name).ID
	applyLink(l, &in, hash)
	srv.st.links = append(srv.st.links, l)
	if !srv.saveOrFail(w) {
		return
	}
	writeJSON(w, http.StatusCreated, viewLink(l))
}

func (srv *server) handleUpdateLink(w http.ResponseWriter, r *http.Request) {
	var in linkInput
	if !readJSON(w, r, &in) {
		return
	}
	hash, err := in.validate(false)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	l := srv.st.linkByID(r.PathValue("id"))
	if l == nil {
		jsonError(w, http.StatusNotFound, "That upload link doesn't exist.")
		return
	}
	if in.Revoked != nil && !*in.Revoked && srv.st.inTrash(l.Folder) {
		jsonError(w, http.StatusConflict, "This link's folder is in the trash. Restore the folder first, then reopen the link.")
		return
	}
	applyLink(l, &in, hash)
	if in.Name != nil {
		if folder := srv.st.items[l.Folder]; folder != nil {
			folder.Name = *in.Name
		}
	}
	if !srv.saveOrFail(w) {
		return
	}
	writeJSON(w, http.StatusOK, viewLink(l))
}

func applyLink(l *Link, in *linkInput, passwordHash *string) {
	if in.Name != nil {
		l.Name = *in.Name
	}
	if in.Quota != nil {
		l.Quota = *in.Quota
	}
	if in.MaxFiles != nil {
		l.MaxFiles = *in.MaxFiles
	}
	if in.ExpiresDays != nil {
		l.Expires = expiresAt(*in.ExpiresDays)
	}
	if passwordHash != nil {
		l.PasswordHash = *passwordHash
	}
	if in.Message != nil {
		l.Message = *in.Message
	}
	if in.Revoked != nil {
		l.Revoked = *in.Revoked
	}
}

func (srv *server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	var in linkInput
	if !readJSON(w, r, &in) {
		return
	}
	hash, err := in.validate(true)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch {
	case in.Folder != nil && in.Files != nil:
		jsonError(w, http.StatusBadRequest, "Share either files or a folder, not both.")
		return
	case in.Folder == nil && (in.Files == nil || len(*in.Files) == 0):
		jsonError(w, http.StatusBadRequest, "Pick at least one file.")
		return
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	s := &Share{ID: newID(), Token: newID(), Name: *in.Name, Created: nowMillis()}
	if in.Folder != nil {
		folder := srv.st.items[*in.Folder]
		switch {
		case folder == nil || folder.Type != "folder":
			jsonError(w, http.StatusBadRequest, "That folder doesn't exist anymore.")
			return
		case folder.ID == rootID:
			// That would hand out everything, including what strangers sent through upload links.
			jsonError(w, http.StatusBadRequest, "My files can't be shared as a whole. Share a folder in it, or pick files.")
			return
		case srv.st.inTrash(folder.ID):
			jsonError(w, http.StatusBadRequest, "That folder is in the trash. Restore it first.")
			return
		}
		s.Folder = folder.ID
	}
	var files []string
	if in.Files != nil {
		files = *in.Files
	}
	for _, id := range files {
		if it := srv.st.items[id]; it == nil || it.Type != "file" {
			jsonError(w, http.StatusBadRequest, "One of the files doesn't exist anymore.")
			return
		}
		if srv.st.inTrash(id) {
			jsonError(w, http.StatusBadRequest, "One of the files is in the trash. Restore it first.")
			return
		}
		if !slices.Contains(s.Files, id) {
			s.Files = append(s.Files, id)
		}
	}
	applyShare(s, &in, hash)
	srv.st.shares = append(srv.st.shares, s)
	if !srv.saveOrFail(w) {
		return
	}
	writeJSON(w, http.StatusCreated, viewShare(s))
}

func (srv *server) handleUpdateShare(w http.ResponseWriter, r *http.Request) {
	var in linkInput
	if !readJSON(w, r, &in) {
		return
	}
	if in.Files != nil || in.Folder != nil {
		jsonError(w, http.StatusBadRequest, "What a share link shares can't be changed. Create a new link instead.")
		return
	}
	hash, err := in.validate(false)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	s := srv.st.shareByID(r.PathValue("id"))
	if s == nil {
		jsonError(w, http.StatusNotFound, "That share link doesn't exist.")
		return
	}
	applyShare(s, &in, hash)
	if !srv.saveOrFail(w) {
		return
	}
	writeJSON(w, http.StatusOK, viewShare(s))
}

func applyShare(s *Share, in *linkInput, passwordHash *string) {
	if in.Name != nil {
		s.Name = *in.Name
	}
	if in.MaxDownloads != nil {
		s.MaxDownloads = *in.MaxDownloads
	}
	if in.ExpiresDays != nil {
		s.Expires = expiresAt(*in.ExpiresDays)
	}
	if passwordHash != nil {
		s.PasswordHash = *passwordHash
	}
	if in.Message != nil {
		s.Message = *in.Message
	}
	if in.Revoked != nil {
		s.Revoked = *in.Revoked
	}
}

// saveOrFail writes the metadata, answering with an error if that fails. Hold st.mu.
func (srv *server) saveOrFail(w http.ResponseWriter) bool {
	if err := srv.st.save(); err != nil {
		jsonError(w, http.StatusInternalServerError, "Couldn't save the change: "+err.Error())
		return false
	}
	return true
}
