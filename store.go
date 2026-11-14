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
	"slices"
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

	// Trashed is when the owner deleted the item, or 0. Only the item they deleted gets it: what's
	// inside a deleted folder is in the trash through that folder, and comes back with it.
	Trashed int64 `json:"trashed,omitempty"`
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

// Share is a share link: anyone with its token can view and download the files in it. It holds
// either a fixed list of files, or one folder with everything in it, also what's added later.
type Share struct {
	ID           string   `json:"id"`
	Token        string   `json:"token"`
	Name         string   `json:"name"`
	Files        []string `json:"files"`
	Folder       string   `json:"folder,omitempty"`
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

// children returns everything directly in a folder, including what's in the trash.
func (st *store) children(parent string) []*Item {
	var out []*Item
	for _, it := range st.items {
		if it.Parent == parent {
			out = append(out, it)
		}
	}
	return out
}

// liveChildren returns what's directly in a folder, leaving out what's in the trash.
func (st *store) liveChildren(parent string) []*Item {
	return slices.DeleteFunc(st.children(parent), func(it *Item) bool { return it.Trashed != 0 })
}

// inTrash says whether an item, or a folder it's in, was deleted.
func (st *store) inTrash(id string) bool {
	for it := st.items[id]; it != nil; it = st.items[it.Parent] {
		if it.Trashed != 0 {
			return true
		}
	}
	return false
}

// purge deletes an item for good: a file's contents, or a folder with everything in it. Call
// dropDangling once afterwards.
func (st *store) purge(it *Item) error {
	if it.Type == "folder" {
		for _, child := range st.children(it.ID) {
			if err := st.purge(child); err != nil {
				return err
			}
		}
	} else if err := os.Remove(st.blobPath(it.ID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	delete(st.items, it.ID)
	return nil
}

// trashedSince returns the items deleted before the given time (Unix milliseconds), or all
// deleted items for 0.
func (st *store) trashedSince(before int64) []*Item {
	var out []*Item
	for _, it := range st.items {
		if it.Trashed != 0 && (before == 0 || it.Trashed < before) {
			out = append(out, it)
		}
	}
	return out
}

// purgeAll deletes items for good, skipping any that went already with a folder they were in,
// and says how many it deleted.
func (st *store) purgeAll(items []*Item) (int, error) {
	deleted := 0
	var err error
	for _, it := range items {
		if st.items[it.ID] != it {
			continue
		}
		if err = st.purge(it); err != nil {
			err = fmt.Errorf("Couldn't delete %s: %w", it.Name, err)
			break
		}
		deleted++
	}
	st.dropDangling()
	return deleted, err
}

// dropDangling forgets what pointed at items that are gone: files in share links, and upload
// links whose folder was deleted for good.
func (st *store) dropDangling() {
	for _, s := range st.shares {
		s.Files = slices.DeleteFunc(s.Files, func(id string) bool { return st.items[id] == nil })
	}
	st.links = slices.DeleteFunc(st.links, func(l *Link) bool { return st.items[l.Folder] == nil })
}

// uniqueName returns name, or "name (2).ext" and so on if the folder already has an item with
// that name. Items in the trash don't count, so their names can be used again.
func (st *store) uniqueName(parent, name string) string {
	taken := map[string]bool{}
	for _, it := range st.liveChildren(parent) {
		taken[strings.ToLower(it.Name)] = true
	}
	for n := 1; ; n++ {
		if candidate := numbered(name, n); !taken[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

// numbered returns name for n = 1, and "name (n).ext" after that.
func numbered(name string, n int) string {
	if n < 2 {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if ext == name { // ".bashrc" has no base name
		base, ext = name, ""
	}
	return fmt.Sprintf("%s (%d)%s", base, n, ext)
}

// subfolder finds the folder called name in parent, or makes it, so files uploaded with the same
// folder path end up together. An uploaded folder adds to a folder of the same name rather than
// becoming "name (2)". Only when a file has that name does it go to "name (2)", or the first
// such name that's a folder or still free, which is the same one for every file. Hold mu.
func (st *store) subfolder(parent, name string) *Item {
	kids := st.liveChildren(parent)
	for n := 1; ; n++ {
		candidate := numbered(name, n)
		taken := false
		for _, it := range kids {
			if strings.EqualFold(it.Name, candidate) {
				if it.Type == "folder" {
					return it
				}
				taken = true
			}
		}
		if !taken {
			it := &Item{ID: newID(), Type: "folder", Parent: parent, Name: candidate, Modified: nowMillis()}
			st.items[it.ID] = it
			return it
		}
	}
}

// nameTaken says whether another item in the folder already has this name, ignoring case and
// what's in the trash.
func (st *store) nameTaken(parent, name, except string) bool {
	for _, it := range st.liveChildren(parent) {
		if it.ID != except && strings.EqualFold(it.Name, name) {
			return true
		}
	}
	return false
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

// underLink says whether folder is an upload link's folder, or in one: whether strangers may have
// put what's in it there. Hold mu.
func (st *store) underLink(folder string) bool {
	for it := st.items[folder]; it != nil; it = st.items[it.Parent] {
		if st.linkByFolder(it.ID) != nil {
			return true
		}
	}
	return false
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

// linkUsage is the space used by, and the number of, files already received through a link,
// including those in folders that were uploaded. Files the owner deleted don't count, so deleting
// them makes room on the link again.
func (st *store) linkUsage(l *Link) (used int64, files int) {
	kids := st.liveKids()
	var walk func(folder string)
	walk = func(folder string) {
		for _, it := range kids[folder] {
			if it.Type == "file" {
				used += it.Size
				files++
			} else {
				walk(it.ID)
			}
		}
	}
	walk(l.Folder)
	return used, files
}

// liveKids lists what's in each folder, leaving out what's in the trash. One pass over all items,
// rather than one per folder, keeps walking large trees quick.
func (st *store) liveKids() map[string][]*Item {
	kids := map[string][]*Item{}
	for _, it := range st.items {
		if it.Trashed == 0 {
			kids[it.Parent] = append(kids[it.Parent], it)
		}
	}
	return kids
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

// sharedFile is a file a share link offers, with the folder it's in relative to the shared
// folder, such as "Day 1/Morning". Path is "" for a file at the top, and for every file of a
// share that holds a list of files.
type sharedFile struct {
	Item
	Path string
}

// shareContents returns the files a share link offers. Files in the trash are left out: they're
// only the owner's to see. A shared folder offers what's in it now, subfolders included: the
// files at the top first, then each subfolder in name order. Hold mu.
func (st *store) shareContents(s *Share) []sharedFile {
	var out []sharedFile
	if s.Folder == "" {
		for _, id := range s.Files {
			if it := st.items[id]; it != nil && !st.inTrash(id) {
				out = append(out, sharedFile{Item: *it})
			}
		}
		return out
	}
	if st.items[s.Folder] == nil || st.inTrash(s.Folder) {
		return nil
	}
	kids := st.liveKids()
	var walk func(folder, path string)
	walk = func(folder, path string) {
		list := kids[folder]
		slices.SortFunc(list, func(a, b *Item) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
		for _, it := range list {
			if it.Type == "file" {
				out = append(out, sharedFile{*it, path})
			}
		}
		for _, it := range list {
			if it.Type == "folder" {
				walk(it.ID, joinPath(path, it.Name))
			}
		}
	}
	walk(s.Folder, "")
	return out
}

// joinPath puts a name after a folder path. Names never contain a slash: cleanName and renaming
// see to that.
func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "/" + name
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

// maxFolderDepth is how many folders inside each other an upload may bring along.
const maxFolderDepth = 64

// cleanPath turns the folder path of an uploaded file, such as "Trip/Day 1", into folder names.
// Each element is cleaned like a file name, and elements without a usable name ("", "." and "..")
// are left out. The folders are only names to look up or make inside the upload's folder, so a
// path can never lead out of it.
func cleanPath(path string) ([]string, error) {
	var out []string
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if name, err := cleanName(part); err == nil {
			out = append(out, name)
		}
	}
	if len(out) > maxFolderDepth {
		return nil, fmt.Errorf("The folders are nested too deeply: at most %d inside each other.", maxFolderDepth)
	}
	return out, nil
}
