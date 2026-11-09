package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Item is a file or a folder. File contents live in <data>/files/<id>.
type Item struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "folder" or "file"
	Parent   string `json:"parent,omitempty"`
	Name     string `json:"name"`
	Size     int64  `json:"size,omitempty"`
	Modified int64  `json:"modified"` // Unix milliseconds, like JavaScript's Date.now()
}

// Link is an upload link: anyone with its token can add files to one folder, up to its limits.
type Link struct {
	ID           string `json:"id"`
	Token        string `json:"token"`
	Name         string `json:"name"`
	Folder       string `json:"folder"`
	Quota        int64  `json:"quota"`
	MaxFiles     int    `json:"maxFiles,omitempty"`
	Created      int64  `json:"created"`
	Expires      int64  `json:"expires,omitempty"` // 0 means never
	PasswordHash string `json:"passwordHash,omitempty"`
	Message      string `json:"message,omitempty"`
	Revoked      bool   `json:"revoked,omitempty"`
}

// Share is a share link: anyone with its token can view and download the files in it.
type Share struct {
	ID           string   `json:"id"`
	Token        string   `json:"token"`
	Name         string   `json:"name"`
	Files        []string `json:"files"`
	Created      int64    `json:"created"`
	Expires      int64    `json:"expires,omitempty"`
	MaxDownloads int      `json:"maxDownloads,omitempty"`
	Downloads    int      `json:"downloads"`
	PasswordHash string   `json:"passwordHash,omitempty"`
	Message      string   `json:"message,omitempty"`
	Revoked      bool     `json:"revoked,omitempty"`
}

const (
	rootID     = "root"
	requestsID = "requests" // parent folder of every upload link's folder
)

type dbFile struct {
	Version   int      `json:"version"`
	Secret    string   `json:"secret"`
	OwnerHash string   `json:"ownerPasswordHash,omitempty"`
	Items     []*Item  `json:"items"`
	Links     []*Link  `json:"links"`
	Shares    []*Share `json:"shares"`
}

// store holds all metadata in memory and writes it to <data>/stash.json after every change.
// Callers hold mu while reading or changing anything.
type store struct {
	mu        sync.Mutex
	dir       string
	secret    []byte // key for signing "unlocked" cookies of password-protected links
	ownerHash string
	items     map[string]*Item
	links     []*Link
	shares    []*Share
}

func openStore(dir string) (*store, error) {
	for _, sub := range []string{"", "files", "uploads"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return nil, err
		}
	}
	st := &store{dir: dir, items: map[string]*Item{}}
	raw, err := os.ReadFile(st.dbPath())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		st.secret = randomBytes(32)
	case err != nil:
		return nil, err
	default:
		var db dbFile
		if err := json.Unmarshal(raw, &db); err != nil {
			return nil, fmt.Errorf("reading %s: %w", st.dbPath(), err)
		}
		if st.secret, err = base64.RawStdEncoding.DecodeString(db.Secret); err != nil || len(st.secret) < 32 {
			return nil, fmt.Errorf("reading %s: invalid secret", st.dbPath())
		}
		st.ownerHash = db.OwnerHash
		for _, it := range db.Items {
			st.items[it.ID] = it
		}
		st.links, st.shares = db.Links, db.Shares
	}
	now := nowMillis()
	if st.items[rootID] == nil {
		st.items[rootID] = &Item{ID: rootID, Type: "folder", Name: "My files", Modified: now}
	}
	if st.items[requestsID] == nil {
		st.items[requestsID] = &Item{ID: requestsID, Type: "folder", Parent: rootID, Name: "Requests", Modified: now}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st, st.save()
}

func (st *store) dbPath() string            { return filepath.Join(st.dir, "stash.json") }
func (st *store) blobPath(id string) string { return filepath.Join(st.dir, "files", id) }
func (st *store) partPath(id string) string { return filepath.Join(st.dir, "uploads", id+".part") }

// save writes the metadata to a temporary file and renames it over the old one,
// so a crash never leaves a half-written file behind.
func (st *store) save() error {
	db := dbFile{Version: 1, Secret: base64.RawStdEncoding.EncodeToString(st.secret), OwnerHash: st.ownerHash, Links: st.links, Shares: st.shares}
	for _, it := range st.items {
		db.Items = append(db.Items, it)
	}
	sort.Slice(db.Items, func(i, j int) bool { return db.Items[i].ID < db.Items[j].ID })
	if db.Links == nil {
		db.Links = []*Link{}
	}
	if db.Shares == nil {
		db.Shares = []*Share{}
	}
	raw, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.dbPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.dbPath()); err != nil {
		return err
	}
	if d, err := os.Open(st.dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

func (st *store) children(parent string) []*Item {
	var out []*Item
	for _, it := range st.items {
		if it.Parent == parent {
			out = append(out, it)
		}
	}
	return out
}

// uniqueName returns name, or "name (2).ext" and so on if the folder already has an item with that name.
func (st *store) uniqueName(parent, name string) string {
	taken := map[string]bool{}
	for _, it := range st.children(parent) {
		taken[strings.ToLower(it.Name)] = true
	}
	if !taken[strings.ToLower(name)] {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if ext == name { // ".bashrc" has no base name
		base, ext = name, ""
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, n, ext)
		if !taken[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

func (st *store) addFolder(parent, name string) *Item {
	it := &Item{ID: newID(), Type: "folder", Parent: parent, Name: st.uniqueName(parent, name), Modified: nowMillis()}
	st.items[it.ID] = it
	return it
}

func (st *store) linkByToken(token string) *Link {
	for _, l := range st.links {
		if tokenEqual(l.Token, token) {
			return l
		}
	}
	return nil
}

func (st *store) linkByID(id string) *Link {
	for _, l := range st.links {
		if l.ID == id {
			return l
		}
	}
	return nil
}

// linkByFolder finds the upload link whose files land in folder, if any.
func (st *store) linkByFolder(folder string) *Link {
	for _, l := range st.links {
		if l.Folder == folder {
			return l
		}
	}
	return nil
}

func (st *store) shareByToken(token string) *Share {
	for _, s := range st.shares {
		if tokenEqual(s.Token, token) {
			return s
		}
	}
	return nil
}

func (st *store) shareByID(id string) *Share {
	for _, s := range st.shares {
		if s.ID == id {
			return s
		}
	}
	return nil
}

// linkUsage is the space used by, and the number of, files already received through a link.
func (st *store) linkUsage(l *Link) (used int64, files int) {
	for _, it := range st.children(l.Folder) {
		if it.Type == "file" {
			used += it.Size
			files++
		}
	}
	return used, files
}

func (st *store) usedBytes() int64 {
	var total int64
	for _, it := range st.items {
		total += it.Size
	}
	return total
}

func linkStatus(l *Link, used int64, files int) string {
	switch {
	case l.Revoked:
		return "revoked"
	case l.Expires != 0 && l.Expires <= nowMillis():
		return "expired"
	case used >= l.Quota || (l.MaxFiles > 0 && files >= l.MaxFiles):
		return "full"
	}
	return "active"
}

func shareStatus(s *Share) string {
	switch {
	case s.Revoked:
		return "revoked"
	case s.Expires != 0 && s.Expires <= nowMillis():
		return "expired"
	case s.MaxDownloads > 0 && s.Downloads >= s.MaxDownloads:
		return "full"
	}
	return "active"
}

/* ---------- Small helpers ---------- */

func nowMillis() int64 { return time.Now().UnixMilli() }

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return b
}

// newID returns a random identifier that is also safe to use as a file name and in URLs.
func newID() string { return base64.RawURLEncoding.EncodeToString(randomBytes(16)) }

// cleanName turns a name from a browser or a command line into something safe to show and store.
// It keeps only the last path element and drops control characters.
func cleanName(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	for len(name) > 255 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	if name == "" || name == "." || name == ".." {
		return "", errors.New("the file needs a name")
	}
	return name, nil
}
