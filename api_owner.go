package main

import (
	"fmt"
	"net/http"
	"os"
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
	return shareView{s.ID, s.Token, s.Name, files, s.Created, s.Expires, s.MaxDownloads, s.Downloads, s.PasswordHash != "", s.Message, s.Revoked}
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
		Origin string `json:"origin"`
	}{Items: []*Item{}, Links: []linkView{}, Shares: []shareView{}, Origin: srv.origin(r)}
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

// handleDeleteItems deletes files. Folders stay, so nothing disappears by surprise.
func (srv *server) handleDeleteItems(w http.ResponseWriter, r *http.Request) {
	var in struct{ IDs []string }
	if !readJSON(w, r, &in) {
		return
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	deleted := 0
	for _, id := range in.IDs {
		it := srv.st.items[id]
		if it == nil || it.Type != "file" {
			continue
		}
		if err := os.Remove(srv.st.blobPath(id)); err != nil && !os.IsNotExist(err) {
			jsonError(w, http.StatusInternalServerError, fmt.Sprintf("Couldn't delete %s.", it.Name))
			return
		}
		delete(srv.st.items, id)
		deleted++
	}
	for _, s := range srv.st.shares {
		s.Files = slices.DeleteFunc(s.Files, func(id string) bool { return srv.st.items[id] == nil })
	}
	if !srv.saveOrFail(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": deleted})
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
	if in.Files == nil || len(*in.Files) == 0 {
		jsonError(w, http.StatusBadRequest, "Pick at least one file.")
		return
	}
	srv.st.mu.Lock()
	defer srv.st.mu.Unlock()
	s := &Share{ID: newID(), Token: newID(), Name: *in.Name, Created: nowMillis()}
	for _, id := range *in.Files {
		if it := srv.st.items[id]; it == nil || it.Type != "file" {
			jsonError(w, http.StatusBadRequest, "One of the files doesn't exist anymore.")
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
	if in.Files != nil {
		jsonError(w, http.StatusBadRequest, "The files of a share link can't be changed. Create a new link instead.")
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
