// Command aeboard is a collaborative whiteboard: a BlockSuite edgeless SPA
// embedded in one Go binary that syncs boards over Yjs (ygo) and keeps them
// in a board store.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aenix-io/aeboard/internal/blobs"
	"github.com/aenix-io/aeboard/internal/boards"
	"github.com/aenix-io/aeboard/internal/ghcli"
	"github.com/aenix-io/aeboard/internal/server"
	"github.com/aenix-io/aeboard/web"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "aeboard:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "version", "--version", "-v":
		fmt.Println(version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	}
	usage()
	return fmt.Errorf("unknown command %q", args[0])
}

func usage() {
	fmt.Fprint(os.Stderr, `aeboard — collaborative whiteboard

Usage:
  aeboard serve [flags]   run the server
  aeboard version         print the version

Run "aeboard serve -h" for the serve flags.
`)
}

func runServe(args []string) error {
	fl := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fl.String("addr", envOr("AEBOARD_ADDR", "127.0.0.1:8766"), "listen address")
	data := fl.String("data", envOr("AEBOARD_DATA", "./data"), "data directory")
	identity := fl.String("identity", envOr("AEBOARD_IDENTITY", "gh"),
		`who edits: "gh" (the account the local gh CLI is logged in as) or "none" (anonymous, names chosen in the browser)`)
	debug := fl.Bool("debug", false, "debug logging")
	if err := fl.Parse(args); err != nil {
		return err
	}
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	store, err := boards.Open(*data)
	if err != nil {
		return fmt.Errorf("open board store: %w", err)
	}
	blobStore, err := blobs.Open(*data)
	if err != nil {
		return fmt.Errorf("open attachment store: %w", err)
	}
	dist, err := web.DistFS()
	if err != nil {
		return err
	}
	srv := server.New(store, blobStore, dist, log)
	switch *identity {
	case "gh":
		// Like `aeman serve`: a local server acts as the gh account.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		u, err := ghcli.CurrentUser(ctx)
		cancel()
		if err != nil {
			log.Warn("no gh identity, editing anonymously (run `gh auth login`, or pass --identity none)", "err", err)
			break
		}
		log.Info("identity from gh", "login", u.Login)
		srv.SetIdentity(server.StaticIdentity(server.Identity{
			Login: u.Login, Name: u.Name, AvatarURL: server.GitHubAvatarURL(u.Login),
		}))
	case "none":
	default:
		return fmt.Errorf("--identity: want gh or none, got %q", *identity)
	}
	httpSrv := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", "http://"+*addr, "data", *data, "version", version)
		errc <- httpSrv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Flush the rooms first (their pending coalesced updates), then stop HTTP.
	if err := srv.Shutdown(sctx); err != nil {
		log.Error("sync shutdown", "err", err)
	}
	if err := httpSrv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
