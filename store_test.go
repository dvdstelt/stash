package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanName(t *testing.T) {
	t.Parallel()
	good := map[string]string{
		"report.pdf":                 "report.pdf",
		"  spaced name.txt  ":        "spaced name.txt",
		"../../etc/passwd":           "passwd",
		`C:\Users\someone\photo.jpg`: "photo.jpg",
		"folder/inner/notes.md":      "notes.md",
		"bell\a.txt":                 "bell.txt",
		"new\nline\r.txt":            "newline.txt",
		"\xffbroken.txt":             "broken.txt",
		"naïve café.txt":             "naïve café.txt",
		".bashrc":                    ".bashrc",
		"...":                        "...",
	}
	for in, want := range good {
		got, err := cleanName(in)
		if err != nil || got != want {
			t.Errorf("cleanName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", ".", "..", "dir/", `dir\`, "\x00\x01", "a/.."} {
		if got, err := cleanName(in); err == nil {
			t.Errorf("cleanName(%q) = %q; want an error", in, got)
		}
	}
}

func TestCleanNameShortensLongNames(t *testing.T) {
	t.Parallel()
	for _, in := range []string{strings.Repeat("a", 300), strings.Repeat("é", 200), strings.Repeat("😀", 100)} {
		got, err := cleanName(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > 255 || !utf8.ValidString(got) || !strings.HasPrefix(in, got) {
			t.Errorf("cleanName of a %d-byte name gave %d bytes, valid UTF-8: %v", len(in), len(got), utf8.ValidString(got))
		}
	}
}

func TestUniqueName(t *testing.T) {
	t.Parallel()
	st, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	add := func(name string) {
		it := &Item{ID: newID(), Type: "file", Parent: rootID, Name: name}
		st.items[it.ID] = it
	}
	tests := []struct {
		taken []string
		name  string
		want  string
	}{
		{nil, "a.txt", "a.txt"},
		{[]string{"a.txt"}, "a.txt", "a (2).txt"},
		{[]string{"a.txt", "a (2).txt"}, "a.txt", "a (3).txt"},
		{[]string{"A.TXT"}, "a.txt", "a (2).txt"},
		{[]string{"notes"}, "notes", "notes (2)"},
		{[]string{".bashrc"}, ".bashrc", ".bashrc (2)"},
		{[]string{"b.txt"}, "a.txt", "a.txt"},
	}
	for _, tt := range tests {
		for id, it := range st.items {
			if it.Parent == rootID && it.Type == "file" {
				delete(st.items, id)
			}
		}
		for _, n := range tt.taken {
			add(n)
		}
		if got := st.uniqueName(rootID, tt.name); got != tt.want {
			t.Errorf("with %v taken, uniqueName(%q) = %q; want %q", tt.taken, tt.name, got, tt.want)
		}
	}
	// Other folders don't count.
	if got := st.uniqueName(requestsID, "a.txt"); got != "a.txt" {
		t.Errorf("uniqueName in another folder = %q; want a.txt", got)
	}
}

func TestOpenStoreStartsEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if root := st.items[rootID]; root == nil || root.Type != "folder" || root.Parent != "" {
		t.Errorf("no root folder: %+v", root)
	}
	if req := st.items[requestsID]; req == nil || req.Type != "folder" || req.Parent != rootID {
		t.Errorf("no Requests folder in the root: %+v", req)
	}
	if len(st.secret) != 32 {
		t.Errorf("secret has %d bytes; want 32", len(st.secret))
	}
	for _, sub := range []string{"files", "uploads", "stash.json"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); err != nil {
			t.Error(err)
		}
	}
}

func TestStoreKeepsEverythingAcrossRestarts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	folder := st.addFolder(rootID, "Photos")
	file := &Item{ID: newID(), Type: "file", Parent: folder.ID, Name: "cat.jpg", Size: 1234, Modified: 42}
	st.items[file.ID] = file
	st.ownerHash = "pbkdf2-sha256$1$c2FsdA$a2V5"
	st.links = append(st.links, &Link{ID: "l1", Token: "tok1", Name: "Link", Folder: folder.ID, Quota: 99, MaxFiles: 3, Expires: 7, PasswordHash: "h", Message: "hi", Revoked: true})
	st.shares = append(st.shares, &Share{ID: "s1", Token: "tok2", Name: "Share", Files: []string{file.ID}, MaxDownloads: 5, Downloads: 2, PasswordHash: "h2", Message: "yo"})
	if err := st.save(); err != nil {
		t.Fatal(err)
	}
	st.mu.Unlock()

	again, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(again.secret) != string(st.secret) {
		t.Error("the secret changed, which would lock everyone out of password-protected links")
	}
	if again.ownerHash != st.ownerHash {
		t.Errorf("owner hash = %q; want %q", again.ownerHash, st.ownerHash)
	}
	if got := again.items[file.ID]; got == nil || *got != *file {
		t.Errorf("file = %+v; want %+v", got, file)
	}
	if got := again.items[folder.ID]; got == nil || got.Name != "Photos" {
		t.Errorf("folder = %+v", got)
	}
	if len(again.links) != 1 || *again.links[0] != *st.links[0] {
		t.Errorf("links = %+v", again.links)
	}
	if len(again.shares) != 1 || again.shares[0].Downloads != 2 || again.shares[0].Files[0] != file.ID || again.shares[0].PasswordHash != "h2" {
		t.Errorf("shares = %+v", again.shares)
	}
}

func TestStoreFilesArePrivate(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix permissions")
	}
	dir := filepath.Join(t.TempDir(), "data")
	if _, err := openStore(dir); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "files"): 0o700, filepath.Join(dir, "stash.json"): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s has permissions %o; want %o", path, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "stash.json.tmp")); !os.IsNotExist(err) {
		t.Errorf("the temporary file is left behind: %v", err)
	}
}

func TestOpenStoreRefusesDamagedMetadata(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"not JSON":     "{this isn't json",
		"short secret": `{"version":1,"secret":"c2hvcnQ"}`,
		"bad secret":   `{"version":1,"secret":"!!!"}`,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "stash.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openStore(dir); err == nil {
			t.Errorf("%s: openStore succeeded; want an error rather than starting with a new secret", name)
		}
		raw, _ := os.ReadFile(filepath.Join(dir, "stash.json"))
		if string(raw) != content {
			t.Errorf("%s: the damaged file was overwritten", name)
		}
	}
}

func TestLinkStatus(t *testing.T) {
	t.Parallel()
	past, future := nowMillis()-1000, nowMillis()+60_000
	tests := []struct {
		link  Link
		used  int64
		files int
		want  string
	}{
		{Link{Quota: 100}, 0, 0, "active"},
		{Link{Quota: 100, Expires: future}, 99, 5, "active"},
		{Link{Quota: 100}, 100, 1, "full"},
		{Link{Quota: 100, MaxFiles: 2}, 10, 2, "full"},
		{Link{Quota: 100, MaxFiles: 2}, 10, 1, "active"},
		{Link{Quota: 100, Expires: past}, 0, 0, "expired"},
		{Link{Quota: 100, Expires: past}, 100, 0, "expired"},
		{Link{Quota: 100, Revoked: true, Expires: past}, 100, 0, "revoked"},
	}
	for _, tt := range tests {
		if got := linkStatus(&tt.link, tt.used, tt.files); got != tt.want {
			t.Errorf("linkStatus(%+v, %d, %d) = %q; want %q", tt.link, tt.used, tt.files, got, tt.want)
		}
	}
}

func TestShareStatus(t *testing.T) {
	t.Parallel()
	past, future := nowMillis()-1000, nowMillis()+60_000
	tests := []struct {
		share Share
		want  string
	}{
		{Share{}, "active"},
		{Share{Downloads: 100}, "active"}, // no limit
		{Share{MaxDownloads: 2, Downloads: 1, Expires: future}, "active"},
		{Share{MaxDownloads: 2, Downloads: 2}, "full"},
		{Share{Expires: past}, "expired"},
		{Share{Expires: past, MaxDownloads: 1, Downloads: 1}, "expired"},
		{Share{Revoked: true, Expires: past}, "revoked"},
	}
	for _, tt := range tests {
		if got := shareStatus(&tt.share); got != tt.want {
			t.Errorf("shareStatus(%+v) = %q; want %q", tt.share, got, tt.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()
	tests := map[int64]string{
		0:                 "0 B",
		999:               "999 B",
		1000:              "1 KB",
		1500:              "1.5 KB",
		12_345:            "12.3 KB",
		123_456:           "123 KB",
		1_000_000:         "1 MB",
		1_234_567:         "1.2 MB",
		3 << 30:           "3.2 GB",
		5_000_000_000_000: "5 TB",
	}
	for n, want := range tests {
		if got := formatBytes(n); got != want {
			t.Errorf("formatBytes(%d) = %q; want %q", n, got, want)
		}
	}
}

func TestSlugify(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"Holiday photos 2025": "holiday-photos-2025",
		"Q3 report: final!!":  "q3-report-final",
		"  --already-ok--  ":  "already-ok",
		"***":                 "files",
		"":                    "files",
		"日本":                  "files",
	}
	for in, want := range tests {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q; want %q", in, got, want)
		}
	}
}

// The share page prints curl commands with file names in them. A name must stay a name when
// someone pastes the command into a shell.
func TestShellQuoteSurvivesAShell(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to try the quoting with")
	}
	names := []string{
		"plain.txt",
		"it's here.txt",
		"$(touch pwned).txt",
		"`touch pwned`.txt",
		"a;b|c&d>e<f.txt",
		`back\slash "double" 'single'.txt`,
		"~star*?[x].txt",
		"$HOME and ${PATH}.txt",
		"''''",
	}
	dir := t.TempDir()
	for _, name := range names {
		cmd := exec.Command(sh, "-c", "printf '%s' "+shellQuote(name))
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("%q: %v", name, err)
			continue
		}
		if string(out) != name {
			t.Errorf("the shell turned %q into %q", name, out)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a quoted name ran a command: %v", entries)
	}
}
