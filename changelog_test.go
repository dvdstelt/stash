package main

import (
	"fmt"
	"io/fs"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestChangelogFormat(t *testing.T) {
	t.Parallel()
	got, err := parseChangelog(`# Changelog

What's new, newest first. This introduction isn't a change.

<!--
How to write this file, for whoever edits it.
## 9.9.9 in a comment isn't a version
-->

## 1.10.0

### Folders
<!-- icon: folder -->
Upload a folder
by dropping it.

A second paragraph.

### Fixes
<!-- icon: check -->
<!-- A note that isn't shown. -->
- A **bold** fix.
- A fix that goes on
  over two lines.
* Stars make lists too.

Text after the list.
- A new list.

## 1.9.0 - 2026-11-14

### #1 on a title, and #19 in the text
Fixes #19.
`)
	if err != nil {
		t.Fatal(err)
	}
	want := []release{
		{Version: "1.10.0", Changes: []change{
			{Title: "Folders", Icon: "folder", Body: []block{{Text: "Upload a folder by dropping it."}, {Text: "A second paragraph."}}},
			{Title: "Fixes", Icon: "check", Body: []block{
				{List: []string{"A **bold** fix.", "A fix that goes on over two lines.", "Stars make lists too."}},
				{Text: "Text after the list."},
				{List: []string{"A new list."}},
			}},
		}},
		{Version: "1.9.0", Date: "2026-11-14", Changes: []change{
			{Title: "#1 on a title, and #19 in the text", Body: []block{{Text: "Fixes #19."}}},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestChangelogMistakes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, text, want string }{
		{"empty", "# Changelog\n\nNothing yet.\n", "no version"},
		{"not a version", "## next\n### A\n", `"next" isn't a version`},
		{"short version", "## 1.3\n### A\n", `"1.3" isn't a version`},
		{"bad date", "## 1.3.0 - 20 Nov 2026\n### A\n", "write the date like 2026-11-14"},
		{"oldest first", "## 1.2.0\n### A\n## 1.3.0\n### B\n", "1.3.0 comes after 1.2.0"},
		{"twice", "## 1.2.0\n### A\n## 1.2.0\n### B\n", "1.2.0 comes after 1.2.0"},
		{"no changes", "## 1.3.0\n## 1.2.0\n### A\n", "1.3.0 has no changes"},
		{"no changes at the end", "## 1.3.0\n### A\n## 1.2.0\n", "1.2.0 has no changes"},
		{"change without version", "### A\n## 1.2.0\n### B\n", "put changes under a ## version"},
		{"change without title", "## 1.2.0\n###\nText\n", "give the change a title"},
		{"text without change", "## 1.2.0\nText\n### A\n", "line 2: put this text under a ### title"},
		{"deeper heading", "## 1.2.0\n### A\n#### B\n", "use ## for a version"},
		{"title in a version", "## 1.2.0\n### A\n# B\n", "use ## for a version"},
		{"icon without change", "## 1.2.0\n<!-- icon: folder -->\n### A\n", "put the icon under a change"},
		{"bad icon", "## 1.2.0\n### A\n<!-- icon: <svg> -->\n", "isn't an icon name"},
		{"open comment", "## 1.2.0\n### A\n<!--\nText\n", "isn't closed"},
		{"text after a comment", "## 1.2.0\n### A\n<!-- note --> Text\n", "put nothing after a comment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseChangelog(tc.text)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v; want one saying %q", err, tc.want)
			}
		})
	}
}

// The changelog that's built in follows the format, names only icons the pages have, and dates
// every version but the newest, which may not be released yet.
func TestBuiltInChangelog(t *testing.T) {
	t.Parallel()
	if version != releases[0].Version {
		t.Errorf("version = %q; want the newest in the changelog, %q", version, releases[0].Version)
	}
	js, err := fs.ReadFile(embeddedUI, "ui/js/ui.js")
	if err != nil {
		t.Fatal(err)
	}
	icons := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s+'?([a-z-]+)'?: '<`).FindAllStringSubmatch(string(js), -1) {
		icons[m[1]] = true
	}
	if !icons["folder"] {
		t.Fatal("couldn't find the icons in ui.js")
	}
	for i, r := range releases {
		if i > 0 && r.Date == "" {
			t.Errorf("%s has no date; only the newest version may be unreleased", r.Version)
		}
		for _, c := range r.Changes {
			if c.Icon != "" && !icons[c.Icon] {
				t.Errorf("%s, %q: there's no icon %q in ui/js/ui.js", r.Version, c.Title, c.Icon)
			}
			if len(c.Body) == 0 {
				t.Errorf("%s, %q: say what the change is under its title", r.Version, c.Title)
			}
		}
	}
}

func TestNewerVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"1.3.0", "1.2.0", true},
		{"1.2.0", "1.3.0", false},
		{"1.2.0", "1.2.0", false},
		{"1.10.0", "1.9.0", true},
		{"2.0.0", "1.99.99", true},
		{"1.2.1", "1.2.0", true},
		{"1.3.0", "", true},
		{"", "1.3.0", false},
		{"1.3.0", "1.3.0-rc.1", true},
		{"v1.3.0", "1.2.0", false},
		{"1.+3.0", "1.2.0", false},
	} {
		if got := newerVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("newerVersion(%q, %q) = %v; want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

/* ---------- What's new ---------- */

// useReleases makes Stash think it's the first of these versions, with one change in each.
func (e *testEnv) useReleases(versions ...string) {
	e.t.Helper()
	var text strings.Builder
	for _, v := range versions {
		fmt.Fprintf(&text, "## %s\n### New in %s\nWhat %s brings.\n\n", v, v, v)
	}
	rs, err := parseChangelog(text.String())
	if err != nil {
		e.t.Fatal(err)
	}
	e.srv.st.mu.Lock()
	e.srv.releases = rs
	e.srv.st.mu.Unlock()
}

type releasesReply struct {
	Version  string
	Releases []struct {
		Version string
		New     bool
		Changes []change
	}
}

func (e *testEnv) releases() releasesReply {
	e.t.Helper()
	var r releasesReply
	e.expect(e.send(e.owner, http.MethodGet, "/api/releases", nil), http.StatusOK).decode(e.t, &r)
	return r
}

// newReleases lists the versions that What's new shows as new.
func (e *testEnv) newReleases() []string {
	e.t.Helper()
	out := []string{}
	for _, r := range e.releases().Releases {
		if r.New {
			out = append(out, r.Version)
		}
	}
	return out
}

func (e *testEnv) wantWhatsNew(want ...string) {
	e.t.Helper()
	st := e.state()
	if st.WhatsNew != (len(want) > 0) {
		e.t.Errorf("whatsNew = %v; want %v", st.WhatsNew, len(want) > 0)
	}
	if want == nil {
		want = []string{}
	}
	if got := e.newReleases(); !reflect.DeepEqual(got, want) {
		e.t.Errorf("new versions = %v; want %v", got, want)
	}
}

func TestWhatsNewAfterAnUpdate(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.useReleases("1.3.0", "1.2.0", "1.1.0")

	if st := e.state(); st.Version != "1.3.0" {
		t.Errorf("version = %q; want 1.3.0", st.Version)
	}
	r := e.releases()
	if r.Version != "1.3.0" || len(r.Releases) != 3 {
		t.Fatalf("releases = %+v; want 1.3.0 and three versions", r)
	}
	if c := r.Releases[0].Changes; len(c) != 1 || c[0].Title != "New in 1.3.0" || c[0].Body[0].Text != "What 1.3.0 brings." {
		t.Errorf("changes in 1.3.0 = %+v", c)
	}

	// The owner used 1.2.0 or older, which didn't remember which version they saw. Everything
	// after 1.2.0 is new to them.
	e.wantWhatsNew("1.3.0")

	// Remind me later changes nothing: What's new opens again the next time. Got it is a change,
	// so it needs the pages' header.
	e.expect(e.send(e.owner, http.MethodPost, "/api/releases/seen", nil), http.StatusForbidden)
	e.wantWhatsNew("1.3.0")
	e.expect(e.api(http.MethodPost, "/api/releases/seen", nil), http.StatusNoContent)
	e.wantWhatsNew()

	// Stash still knows after a restart.
	e = newEnvWith(t, config{DataDir: e.srv.cfg.DataDir})
	e.useReleases("1.3.0", "1.2.0", "1.1.0")
	e.wantWhatsNew()

	// Skipping a version shows what both brought, and not again what the owner saw.
	e.useReleases("1.5.0", "1.4.0", "1.3.0", "1.2.0", "1.1.0")
	e.wantWhatsNew("1.5.0", "1.4.0")
	e.expect(e.api(http.MethodPost, "/api/releases/seen", nil), http.StatusNoContent)

	// Going back to an older version shows nothing, and Got it there doesn't make 1.5.0 new again.
	e.useReleases("1.4.0", "1.3.0")
	e.wantWhatsNew()
	e.expect(e.api(http.MethodPost, "/api/releases/seen", nil), http.StatusNoContent)
	e.useReleases("1.5.0", "1.4.0", "1.3.0")
	e.wantWhatsNew()
}

// An owner coming from 1.2.0 to a later fix still sees what the version before it brought, not
// only the fix.
func TestWhatsNewAfterSkippingFrom120(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.useReleases("1.3.1", "1.3.0", "1.2.0", "1.1.0")
	e.wantWhatsNew("1.3.1", "1.3.0")
	e.expect(e.api(http.MethodPost, "/api/releases/seen", nil), http.StatusNoContent)
	e.wantWhatsNew()
}

// Someone who just installed Stash has no update to catch up on.
func TestNoWhatsNewAfterInstalling(t *testing.T) {
	t.Parallel()
	e := startServer(t, config{})
	e.useReleases("1.3.0", "1.2.0")
	e.expect(e.sendJSON(e.owner, http.MethodPost, "/api/setup", map[string]string{"token": e.srv.setupToken, "password": "a good long password"}), http.StatusNoContent)
	e.wantWhatsNew()
}

func TestWhatsNewIsForTheOwner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	r := e.get("/api/releases")
	e.expect(r, http.StatusUnauthorized)
	wantNoBody(t, r, version)
	e.expect(e.sendJSON(newBrowser(), http.MethodPost, "/api/releases/seen", nil, "X-Stash", "1"), http.StatusUnauthorized)
	var unseen []string // what the built-in changelog has after 1.2.0
	for _, rel := range releases {
		if newerVersion(rel.Version, lastWithoutSeen) {
			unseen = append(unseen, rel.Version)
		}
	}
	e.wantWhatsNew(unseen...)
}

// The pages get the changelog that's built in, which is what the running version says.
func TestWhatsNewShowsTheChangelog(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	r := e.releases()
	if r.Version != version || len(r.Releases) != len(releases) || len(r.Releases[0].Changes) != len(releases[0].Changes) {
		t.Errorf("releases = %+v; want the %d versions in CHANGELOG.md, newest %s", r, len(releases), version)
	}
	if st := e.state(); st.Version != version {
		t.Errorf("version = %q; want %q", st.Version, version)
	}
}
