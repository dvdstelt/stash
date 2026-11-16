package main

// The changelog. Stash embeds CHANGELOG.md, takes its own version from the newest entry, and
// shows the entries to the owner.

import (
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed CHANGELOG.md
var changelogText string

// releases holds every version in CHANGELOG.md, newest first. A changelog that doesn't follow the
// format stops Stash at startup, and fails the tests long before that.
var releases = mustParseChangelog(changelogText)

// version is the version that's running: the newest one in the changelog.
var version = releases[0].Version

type release struct {
	Version string   `json:"version"`
	Date    string   `json:"date,omitempty"` // YYYY-MM-DD; empty until the version is released
	Changes []change `json:"changes"`
}

type change struct {
	Title string  `json:"title"`
	Icon  string  `json:"icon,omitempty"`
	Body  []block `json:"body"`
}

// block is a paragraph or a bulleted list. Its text may contain **bold** and `code`, which the
// pages show as such.
type block struct {
	Text string   `json:"text,omitempty"`
	List []string `json:"list,omitempty"`
}

func mustParseChangelog(text string) []release {
	rs, err := parseChangelog(text)
	if err != nil {
		panic("CHANGELOG.md: " + err.Error())
	}
	return rs
}

var (
	iconName = regexp.MustCompile(`^[a-z][a-z-]*$`)
	heading  = regexp.MustCompile(`^(#{1,6})(?:\s+(.*))?$`)
)

// parseChangelog reads the format described at the top of CHANGELOG.md. "## 1.2.0 - 2026-11-14"
// starts a version and "### Title" a change in it. The lines below a change are its paragraphs
// and "- " lists. Everything before the first version is left out, and so are HTML comments,
// apart from "<!-- icon: name -->", which picks the change's icon.
func parseChangelog(text string) ([]release, error) {
	var out []release
	inComment := false
	blank := false // the line before was empty, so text starts a new paragraph
	last := func() *release { return &out[len(out)-1] }
	current := func() *change {
		if len(out) == 0 || len(last().Changes) == 0 {
			return nil
		}
		return &last().Changes[len(last().Changes)-1]
	}

	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		line = strings.TrimSpace(line)
		if inComment {
			inComment = !strings.Contains(line, "-->")
			continue
		}
		if comment, ok := strings.CutPrefix(line, "<!--"); ok {
			comment, closed := strings.CutSuffix(comment, "-->")
			if !closed {
				if strings.Contains(comment, "-->") {
					return nil, fmt.Errorf("line %d: put nothing after a comment on its line", n)
				}
				inComment = true
				continue
			}
			if name, ok := strings.CutPrefix(strings.TrimSpace(comment), "icon:"); ok {
				name = strings.TrimSpace(name)
				if current() == nil {
					return nil, fmt.Errorf("line %d: put the icon under a change's ### title", n)
				}
				if !iconName.MatchString(name) {
					return nil, fmt.Errorf("line %d: %q isn't an icon name such as folder", n, name)
				}
				current().Icon = name
			}
			continue
		}

		if line == "" {
			blank = true
			continue
		}
		if m := heading.FindStringSubmatch(line); m != nil {
			if err := addHeading(&out, len(m[1]), strings.TrimSpace(m[2])); err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
			blank = false
			continue
		}

		switch {
		case len(out) == 0:
			// The title and introduction above the first version.
		case current() == nil:
			return nil, fmt.Errorf("line %d: put this text under a ### title", n)
		default:
			c := current()
			var prev *block
			if len(c.Body) > 0 {
				prev = &c.Body[len(c.Body)-1]
			}
			item, bullet := strings.CutPrefix(line, "- ")
			if !bullet {
				item, bullet = strings.CutPrefix(line, "* ")
			}
			switch {
			case bullet && prev != nil && prev.List != nil:
				prev.List = append(prev.List, item)
			case bullet:
				c.Body = append(c.Body, block{List: []string{item}})
			case blank || prev == nil:
				c.Body = append(c.Body, block{Text: line})
			case prev.List != nil: // a list item that goes on over more lines
				prev.List[len(prev.List)-1] += " " + line
			default:
				prev.Text += " " + line
			}
		}
		blank = false
	}

	if inComment {
		return nil, fmt.Errorf("a comment isn't closed with -->")
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("there's no version in it: start one with ## 1.0.0")
	}
	if len(last().Changes) == 0 {
		return nil, fmt.Errorf("%s has no changes", last().Version)
	}
	return out, nil
}

// addHeading starts a version (level 2) or a change in the newest version (level 3). The title
// (level 1) is only allowed above the first version.
func addHeading(out *[]release, level int, text string) error {
	var last *release
	if len(*out) > 0 {
		last = &(*out)[len(*out)-1]
	}
	switch {
	case level == 1 && last == nil:
		return nil
	case level == 2:
		v, date, dated := strings.Cut(text, " - ")
		v, date = strings.TrimSpace(v), strings.TrimSpace(date)
		if _, ok := parseVersion(v); !ok {
			return fmt.Errorf("%q isn't a version such as 1.2.0", v)
		}
		if dated {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return fmt.Errorf("write the date like 2026-11-14, not %q", date)
			}
		}
		if last != nil && !newerVersion(last.Version, v) {
			return fmt.Errorf("%s comes after %s, but the newest version goes first", v, last.Version)
		}
		if last != nil && len(last.Changes) == 0 {
			return fmt.Errorf("%s has no changes", last.Version)
		}
		*out = append(*out, release{Version: v, Date: date, Changes: []change{}})
	case level == 3 && last == nil:
		return fmt.Errorf("put changes under a ## version")
	case level == 3 && text == "":
		return fmt.Errorf("give the change a title")
	case level == 3:
		last.Changes = append(last.Changes, change{Title: text, Body: []block{}})
	default:
		return fmt.Errorf("use ## for a version and ### for a change")
	}
	return nil
}

// parseVersion reads a version such as 1.12.0.
func parseVersion(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			return v, false
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// newerVersion says whether version a comes after b. Anything is newer than a b that isn't a
// version, such as an empty string.
func newerVersion(a, b string) bool {
	va, ok := parseVersion(a)
	if !ok {
		return false
	}
	vb, ok := parseVersion(b)
	if !ok {
		return true
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}
