package main

// Checking files received through upload links for viruses. Stash doesn't scan anything itself:
// it sends each file to ClamAV's clamd over its socket, with the INSTREAM command, and reads the
// verdict. clamd runs on its own, usually in a container of its own, and keeps its virus
// definitions up to date with freshclam.

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"slices"
	"strings"
	"time"
)

// What the virus scanner said about a file, in Item.Scan.
const (
	scanWaiting  = "waiting"   // not scanned yet: clamd couldn't be reached, or it's the next in line
	scanClean    = "clean"     // no virus found
	scanVirus    = "virus"     // a virus was found; Item.Virus names it
	scanTooLarge = "too-large" // larger than clamd takes (its StreamMaxLength), so not scanned
	scanFailed   = "failed"    // clamd couldn't scan it, also after trying again
)

const (
	clamdChunk     = 64 << 10
	clamdDialWait  = 10 * time.Second
	clamdIdle      = 2 * time.Minute // for each piece of the file clamd has to take in
	clamdVerdict   = 5 * time.Minute // for the verdict after the last byte: big archives take a while
	scanRetryAfter = time.Minute     // how often files still waiting are tried again
	scanTries      = 3               // a file that breaks off the scan this often is marked failed
)

type scanner struct {
	addr   string // host:port, or the path of clamd's socket
	refuse bool   // turn away files with a virus, instead of keeping and marking them
	wake   chan struct{}

	// Only the scan loop uses these.
	tries map[string]int // per item: scans that broke off halfway
	down  bool           // clamd couldn't be reached last time, which has been logged
}

func newScanner(addr, action string) *scanner {
	if addr == "" {
		return nil
	}
	return &scanner{addr: addr, refuse: action == "refuse", wake: make(chan struct{}, 1), tries: map[string]int{}}
}

// poke makes the scan loop look for waiting files now rather than in a minute.
func (sc *scanner) poke() {
	select {
	case sc.wake <- struct{}{}:
	default:
	}
}

type scanResult struct {
	state string
	virus string
}

// unreachable means clamd couldn't be reached at all, as opposed to a scan that broke off.
type unreachable struct{ err error }

func (e *unreachable) Error() string { return e.err.Error() }

func (sc *scanner) dial() (net.Conn, error) {
	network := "tcp"
	if strings.Contains(sc.addr, "/") {
		network = "unix"
	}
	conn, err := net.DialTimeout(network, sc.addr, clamdDialWait)
	if err != nil {
		return nil, &unreachable{err}
	}
	return conn, nil
}

// check sends a file to clamd and returns its verdict. An error means there is no verdict yet:
// try again later. An unreachable error says clamd isn't there at all.
func (sc *scanner) check(path string) (scanResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return scanResult{state: scanFailed}, nil
	}
	defer f.Close()
	conn, err := sc.dial()
	if err != nil {
		return scanResult{}, err
	}
	defer conn.Close()

	// The z prefix makes clamd end its reply with a zero byte.
	conn.SetDeadline(time.Now().Add(clamdIdle))
	if _, err := io.WriteString(conn, "zINSTREAM\x00"); err != nil {
		return scanResult{}, &unreachable{err}
	}
	// Each piece goes out as its length in 4 bytes, then the bytes; a length of 0 ends the file.
	buf := make([]byte, 4+clamdChunk)
	for {
		n, readErr := io.ReadFull(f, buf[4:])
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return scanResult{state: scanFailed}, nil // never let clamd judge part of a file
		}
		if n > 0 {
			binary.BigEndian.PutUint32(buf, uint32(n))
			conn.SetDeadline(time.Now().Add(clamdIdle))
			if _, err := conn.Write(buf[:4+n]); err != nil {
				// clamd stops reading once a file is larger than it takes, after saying so.
				return verdict(conn, err)
			}
		}
		if readErr != nil {
			break
		}
	}
	conn.SetDeadline(time.Now().Add(clamdVerdict))
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return verdict(conn, err)
	}
	return verdict(conn, nil)
}

// verdict reads clamd's reply, such as "stream: OK" or "stream: Eicar-Signature FOUND".
// sendErr is why sending the file stopped early, if it did.
func verdict(conn net.Conn, sendErr error) (scanResult, error) {
	if sendErr != nil {
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	}
	text, err := readReply(conn)
	if text = strings.TrimSpace(text); err != nil || text == "" {
		if sendErr != nil {
			err = sendErr
		} else if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return scanResult{}, fmt.Errorf("clamd broke off the scan: %w", err)
	}
	switch {
	case text == "stream: OK":
		return scanResult{state: scanClean}, nil
	case strings.HasPrefix(text, "stream: ") && strings.HasSuffix(text, " FOUND"):
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, "stream: "), " FOUND"))
		if name == "" {
			name = "a virus"
		}
		return scanResult{state: scanVirus, virus: name}, nil
	case strings.Contains(text, "size limit exceeded"):
		return scanResult{state: scanTooLarge}, nil
	}
	return scanResult{}, fmt.Errorf("clamd answered %q", text)
}

// readReply reads one reply from clamd, up to the zero byte that ends it.
func readReply(conn net.Conn) (string, error) {
	var reply []byte
	buf := make([]byte, 512)
	for len(reply) < 4096 {
		n, err := conn.Read(buf)
		reply = append(reply, buf[:n]...)
		if i := bytes.IndexByte(reply, 0); i >= 0 {
			return string(reply[:i]), nil
		}
		if err == io.EOF && len(reply) > 0 {
			return string(reply), nil
		}
		if err != nil {
			return "", err
		}
	}
	return string(reply), nil
}

// scanLoop scans files that are waiting for the virus scanner, as they arrive, and tries again
// every minute when clamd couldn't be reached.
func (srv *server) scanLoop(ctx context.Context) {
	sc := srv.scan
	if conn, err := sc.dial(); err != nil {
		sc.down = true
		log.Printf("The virus scanner at %s can't be reached: %v. Received files wait until it can.", sc.addr, err)
	} else {
		conn.Close()
		log.Printf("Checking files received through upload links for viruses with clamd at %s.", sc.addr)
	}
	tick := time.NewTicker(scanRetryAfter)
	defer tick.Stop()
	for {
		srv.scanWaiting(ctx)
		select {
		case <-ctx.Done():
			return
		case <-sc.wake:
		case <-tick.C:
		}
	}
}

// scanWaiting scans every file that's waiting for the virus scanner, oldest first.
func (srv *server) scanWaiting(ctx context.Context) {
	sc := srv.scan
	srv.st.mu.Lock()
	var waiting []Item
	for _, it := range srv.st.items {
		if it.Scan == scanWaiting {
			waiting = append(waiting, *it)
		}
	}
	srv.st.mu.Unlock()
	slices.SortFunc(waiting, func(a, b Item) int { return cmp.Compare(a.Modified, b.Modified) })

	for _, it := range waiting {
		if ctx.Err() != nil {
			return
		}
		res, err := sc.check(srv.st.blobPath(it.ID))
		var down *unreachable
		if errors.As(err, &down) {
			if !sc.down {
				sc.down = true
				log.Printf("The virus scanner at %s can't be reached: %v. Received files wait until it can.", sc.addr, err)
			}
			return
		}
		if sc.down {
			sc.down = false
			log.Printf("The virus scanner at %s is back.", sc.addr)
		}
		if err != nil {
			if sc.tries[it.ID]++; sc.tries[it.ID] < scanTries {
				continue
			}
			log.Printf("The virus scanner couldn't check %q: %v", it.Name, err)
			res = scanResult{state: scanFailed}
		}
		delete(sc.tries, it.ID)
		srv.st.mu.Lock()
		if cur := srv.st.items[it.ID]; cur != nil && cur.Scan == scanWaiting {
			cur.Scan, cur.Virus = res.state, res.virus
			if err := srv.st.save(); err != nil {
				log.Printf("saving metadata: %v", err)
			}
			if res.state == scanVirus {
				from := ""
				if l := srv.st.linkAbove(cur.Parent); l != nil {
					from = fmt.Sprintf(" from upload link %q", l.Name)
				}
				log.Printf("Virus found in %q%s: %s. It's marked, and can only be downloaded.", cur.Name, from, res.virus)
			}
		}
		srv.st.mu.Unlock()
	}
}
