package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testVirus stands in for a virus: the fake clamd finds one in every file that contains it.
const (
	testVirus     = "pretend this is a virus"
	testVirusName = "Stash.Test.Virus-1"
)

// fakeClamd stands in for ClamAV's clamd. It speaks the INSTREAM command, finds a virus in files
// containing testVirus, and turns away files larger than limit, as clamd's StreamMaxLength does.
type fakeClamd struct {
	addr   string
	limit  int
	broken atomic.Bool // hang up halfway through every scan
	scans  atomic.Int32
}

// startClamd starts a fake clamd at addr, or on a free port when addr is "".
func startClamd(t *testing.T, addr string, limit int) *fakeClamd {
	t.Helper()
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	c := &fakeClamd{addr: ln.Addr().String(), limit: limit}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go c.serve(conn)
		}
	}()
	return c
}

// freeAddr is an address where nothing listens, until a test starts something there.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func (c *fakeClamd) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	if cmd, err := r.ReadString(0); err != nil || cmd != "zINSTREAM\x00" {
		conn.Write([]byte("UNKNOWN COMMAND\x00"))
		return
	}
	if c.broken.Load() {
		return
	}
	var data []byte
	for {
		var size [4]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint32(size[:]))
		if n == 0 {
			break
		}
		if len(data)+n > c.limit {
			conn.Write([]byte("INSTREAM size limit exceeded. ERROR\x00"))
			return
		}
		chunk := make([]byte, n)
		if _, err := io.ReadFull(r, chunk); err != nil {
			return
		}
		data = append(data, chunk...)
	}
	c.scans.Add(1)
	if bytes.Contains(data, []byte(testVirus)) {
		conn.Write([]byte("stream: " + testVirusName + " FOUND\x00"))
		return
	}
	conn.Write([]byte("stream: OK\x00"))
}

// newScanEnv starts a Stash that checks received files with clamd at addr.
func newScanEnv(t *testing.T, addr, action string) *testEnv {
	t.Helper()
	e := newEnvWith(t, config{Clamd: addr, VirusAction: action})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		e.srv.scanLoop(ctx)
		close(stopped)
	}()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	return e
}

// scanned waits until the virus scanner is done with a file, and returns the file.
func (e *testEnv) scanned(id string) Item {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		it := e.item(id)
		if it == nil {
			e.t.Fatalf("no item %s", id)
		}
		if it.Scan != scanWaiting {
			return *it
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("%s is still waiting for the virus scanner", it.Name)
		}
		e.srv.scan.poke()
		time.Sleep(20 * time.Millisecond)
	}
}

// itemNamed finds a file in a folder by name.
func (e *testEnv) itemNamed(folder, name string) Item {
	e.t.Helper()
	it, ok := e.filesIn(folder)[name]
	if !ok {
		e.t.Fatalf("%s isn't in the folder; it has %s", name, e.listing(folder))
	}
	return it
}

func TestVirusScanMarksFiles(t *testing.T) {
	t.Parallel()
	clamd := startClamd(t, "", 1<<20)
	e := newScanEnv(t, clamd.addr, "mark")
	l := e.createLink(nil)

	// From the command line, as curl -T sends it, and from the upload page.
	r := e.expect(e.put(l.Token, "invoice.exe", strings.NewReader("MZ "+testVirus)), http.StatusOK)
	wantBody(t, r, "Uploaded invoice.exe")
	e.expect(e.put(l.Token, "notes.txt", strings.NewReader("just notes")), http.StatusOK)
	photo := e.uploadThrough(newBrowser(), l.Token, "Trip", "photo.jpg", "\xff\xd8 "+testVirus)
	mine := e.upload(rootID, "mine.txt", []byte("the owner's own "+testVirus))

	virus := e.scanned(e.itemNamed(l.Folder, "invoice.exe").ID)
	if virus.Scan != scanVirus || virus.Virus != testVirusName {
		t.Errorf("invoice.exe: scan %q, virus %q; want %q found", virus.Scan, virus.Virus, testVirusName)
	}
	if clean := e.scanned(e.itemNamed(l.Folder, "notes.txt").ID); clean.Scan != scanClean || clean.Virus != "" {
		t.Errorf("notes.txt: scan %q, virus %q; want clean", clean.Scan, clean.Virus)
	}
	if got := e.scanned(photo.ID); got.Scan != scanVirus {
		t.Errorf("photo.jpg in an uploaded folder: scan %q; want a virus", got.Scan)
	}
	if got := e.item(mine.ID); got.Scan != "" {
		t.Errorf("the owner's own file: scan %q; want it left alone", got.Scan)
	}
	if st := e.state(); st.Scanner != "mark" {
		t.Errorf("the owner's pages get scanner %q; want mark", st.Scanner)
	}

	// The owner can still get a marked file, but only as a download, even a photo.
	r = e.expect(e.send(e.owner, http.MethodGet, "/api/files/"+photo.ID+"/content", nil), http.StatusOK)
	if r.header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(r.header.Get("Content-Disposition"), "attachment") {
		t.Errorf("photo.jpg with a virus: %q, %q; want a download", r.header.Get("Content-Type"), r.header.Get("Content-Disposition"))
	}

	// Share links leave marked files out: on the page, the command line, the zip, and by ID.
	files := e.createShare(nil, virus, e.itemNamed(l.Folder, "notes.txt"))
	var folder shareView
	e.expect(e.api(http.MethodPost, "/api/shares", map[string]any{"name": "Everything", "folder": l.Folder}), http.StatusCreated).decode(t, &folder)
	for _, s := range []shareView{files, folder} {
		var info shareInfo
		e.expect(e.get("/api/public/s/"+s.Token), http.StatusOK).decode(t, &info)
		for _, f := range info.Files {
			if f.Name == "invoice.exe" || f.Name == "photo.jpg" {
				t.Errorf("share %q offers %s, which has a virus", s.Name, f.Name)
			}
		}
		if len(info.Files) != 1 || info.Files[0].Name != "notes.txt" {
			t.Errorf("share %q offers %+v; want only notes.txt", s.Name, info.Files)
		}
		wantNoBody(t, e.get("/s/"+s.Token), "invoice.exe", "photo.jpg")
		if got := readZip(t, e.expect(e.get("/s/"+s.Token+"/all.zip"), http.StatusOK).body); len(got) != 1 || got["notes.txt"] == "" {
			t.Errorf("share %q zip = %v; want only notes.txt", s.Name, got)
		}
		e.expect(e.get(e.fileURL(s, virus)), http.StatusNotFound)
	}
	e.checkNothingReserved()
}

func TestVirusScanRefusesFiles(t *testing.T) {
	t.Parallel()
	clamd := startClamd(t, "", 1<<20)
	e := newScanEnv(t, clamd.addr, "refuse")
	l := e.createLink(map[string]any{"maxFiles": 2})

	// curl asks first for larger files, PowerShell and wget send everything; both get the reason.
	for _, header := range [][]string{nil, {"Expect", "100-continue"}} {
		r := e.expect(e.put(l.Token, "invoice.exe", strings.NewReader("MZ "+testVirus), header...), http.StatusUnprocessableEntity)
		wantBody(t, r, "Not accepted: the virus scanner found "+testVirusName+" in this file.")
	}
	var started struct{ ID string }
	browser := newBrowser()
	e.expect(e.sendJSON(browser, http.MethodPost, "/api/public/d/"+l.Token+"/uploads", map[string]any{"name": "photo.jpg", "size": 3 + len(testVirus)}), http.StatusCreated).decode(t, &started)
	r := e.expect(e.send(browser, http.MethodPut, "/api/public/d/"+l.Token+"/uploads/"+started.ID+"?offset=0", strings.NewReader("\xff\xd8 "+testVirus)), http.StatusUnprocessableEntity)
	if msg, _ := r.fields(t)["error"].(string); !strings.Contains(msg, testVirusName) {
		t.Errorf("the upload page gets %q; want the virus named", msg)
	}
	if got := e.listing(l.Folder); got != "" {
		t.Errorf("the link's folder holds %s; want nothing", got)
	}
	e.checkNothingReserved()

	// Refused files don't count against the link's limits, and clean files go in, already scanned.
	r = e.expect(e.put(l.Token, "notes.txt", strings.NewReader("just notes")), http.StatusOK)
	wantBody(t, r, "Uploaded notes.txt")
	if got := e.item(e.itemNamed(l.Folder, "notes.txt").ID); got.Scan != scanClean {
		t.Errorf("notes.txt: scan %q; want clean as soon as it's in", got.Scan)
	}
	e.uploadThrough(browser, l.Token, "", "more.txt", "more notes")
	if st := e.state(); st.Scanner != "refuse" {
		t.Errorf("the owner's pages get scanner %q; want refuse", st.Scanner)
	}
}

// Files that come in while clamd can't be reached wait for it, also when files with a virus
// are refused: by the time clamd is back, the sender is gone, so the file is marked instead.
func TestVirusScannerUnreachable(t *testing.T) {
	t.Parallel()
	addr := freeAddr(t)
	e := newScanEnv(t, addr, "refuse")
	l := e.createLink(nil)

	r := e.expect(e.put(l.Token, "invoice.exe", strings.NewReader("MZ "+testVirus)), http.StatusOK)
	wantBody(t, r, "Uploaded invoice.exe")
	f := e.itemNamed(l.Folder, "invoice.exe")
	e.srv.scan.poke()
	time.Sleep(50 * time.Millisecond)
	if got := e.item(f.ID); got.Scan != scanWaiting {
		t.Fatalf("scan %q while clamd is away; want waiting", got.Scan)
	}

	startClamd(t, addr, 1<<20)
	if got := e.scanned(f.ID); got.Scan != scanVirus || got.Virus != testVirusName {
		t.Errorf("once clamd is back: scan %q, virus %q; want it marked", got.Scan, got.Virus)
	}
}

// clamd only takes files up to its StreamMaxLength. Larger files are kept, marked as not scanned.
func TestVirusScanTooLarge(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"mark", "refuse"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			clamd := startClamd(t, "", 100<<10)
			e := newScanEnv(t, clamd.addr, action)
			l := e.createLink(nil)
			e.expect(e.put(l.Token, "big.bin", bytes.NewReader(make([]byte, 300<<10))), http.StatusOK)
			if got := e.scanned(e.itemNamed(l.Folder, "big.bin").ID); got.Scan != scanTooLarge {
				t.Errorf("scan %q; want too-large", got.Scan)
			}
		})
	}
}

// A file that clamd keeps breaking off is tried a few times, then marked as failed, so it doesn't
// hold up the files after it.
func TestVirusScanFailsAfterTries(t *testing.T) {
	t.Parallel()
	clamd := startClamd(t, "", 1<<20)
	clamd.broken.Store(true)
	e := newScanEnv(t, clamd.addr, "mark")
	l := e.createLink(nil)
	e.expect(e.put(l.Token, "odd.bin", strings.NewReader("odd")), http.StatusOK)
	if got := e.scanned(e.itemNamed(l.Folder, "odd.bin").ID); got.Scan != scanFailed {
		t.Errorf("scan %q; want failed", got.Scan)
	}

	clamd.broken.Store(false)
	e.expect(e.put(l.Token, "next.txt", strings.NewReader("fine")), http.StatusOK)
	if got := e.scanned(e.itemNamed(l.Folder, "next.txt").ID); got.Scan != scanClean {
		t.Errorf("the next file: scan %q; want clean", got.Scan)
	}
}

// Without -clamd nothing is scanned, and files don't wait for a scanner that isn't there.
func TestNoVirusScanner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	l := e.createLink(nil)
	e.expect(e.put(l.Token, "invoice.exe", strings.NewReader("MZ "+testVirus)), http.StatusOK)
	if got := e.item(e.itemNamed(l.Folder, "invoice.exe").ID); got.Scan != "" {
		t.Errorf("scan %q; want none", got.Scan)
	}
	if st := e.state(); st.Scanner != "" {
		t.Errorf("scanner %q; want none", st.Scanner)
	}
}
