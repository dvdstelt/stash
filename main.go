// Stash is a small self-hosted place for your files. Other people get upload links, each with
// its own size limit, or share links to download files you picked. Uploads work from a browser
// and from the command line on Windows, Linux and macOS.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type config struct {
	Listen         string
	DataDir        string
	PublicURL      string
	TrustedProxies []netip.Prefix
	UIDir          string
	TrashDays      int
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "reset-password" {
		if err := resetPassword(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	cfg, err := loadConfig("stash", os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	srv, err := newServer(cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.janitor(ctx)

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No overall read or write timeout: uploads and downloads can take hours.
		// Upload handlers instead extend a read deadline whenever data arrives.
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("Stash is listening on %s, storing data in %s", cfg.Listen, cfg.DataDir)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func loadConfig(name string, args []string) (config, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var cfg config
	fs.StringVar(&cfg.Listen, "listen", env("STASH_LISTEN", ":8080"), "address to listen on")
	fs.StringVar(&cfg.DataDir, "data", env("STASH_DATA", "data"), "directory for files and metadata")
	fs.StringVar(&cfg.PublicURL, "public-url", env("STASH_PUBLIC_URL", ""), "base URL used in links you hand out, e.g. https://files.example.com (default: the address you use to open Stash)")
	proxies := fs.String("trusted-proxies", env("STASH_TRUSTED_PROXIES", ""), "comma-separated IPs or CIDRs of reverse proxies whose X-Forwarded-For and X-Forwarded-Proto headers are trusted")
	fs.StringVar(&cfg.UIDir, "ui-dir", env("STASH_UI_DIR", ""), "serve the web pages from this directory instead of the built-in copy (for development)")
	trashDays := fs.String("trash-days", env("STASH_TRASH_DAYS", "30"), "days that deleted files and folders stay in the trash before they're deleted for good")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	days, err := strconv.Atoi(strings.TrimSpace(*trashDays))
	if err != nil || days < 1 || days > 3650 {
		return cfg, fmt.Errorf("trash days %q: use a whole number of days from 1 to 3650", *trashDays)
	}
	cfg.TrashDays = days
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	for _, p := range strings.Split(*proxies, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			addr, err := netip.ParseAddr(p)
			if err != nil {
				return cfg, fmt.Errorf("trusted proxy %q: %w", p, err)
			}
			addr = addr.Unmap()
			cfg.TrustedProxies = append(cfg.TrustedProxies, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(p)
		if err != nil {
			return cfg, fmt.Errorf("trusted proxy %q: %w", p, err)
		}
		cfg.TrustedProxies = append(cfg.TrustedProxies, prefix.Masked())
	}
	return cfg, nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

// resetPassword removes the owner password so the next start prints a new setup link.
// Run it while Stash is stopped; a running server would overwrite the change.
func resetPassword(args []string) error {
	cfg, err := loadConfig("stash reset-password", args)
	if err != nil {
		return err
	}
	st, err := openStore(cfg.DataDir)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.ownerHash = ""
	if err := st.save(); err != nil {
		return err
	}
	fmt.Println("The password is removed. Start Stash and open the setup link it prints to choose a new one.")
	return nil
}
