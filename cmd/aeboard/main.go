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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/aenix-io/aeboard/internal/blobs"
	"github.com/aenix-io/aeboard/internal/boards"
	"github.com/aenix-io/aeboard/internal/ghcli"
	"github.com/aenix-io/aeboard/internal/gitbackend"
	"github.com/aenix-io/aeboard/internal/gitstore"
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
	repoFlag := fl.String("repo", envOr("AEBOARD_REPO", ""),
		"git repository the boards live in: a local path or an https URL (empty: plain files under --data)")
	branch := fl.String("branch", envOr("AEBOARD_BRANCH", "main"), "branch of --repo the boards live on")
	fetchEvery := fl.Duration("fetch-interval", 30*time.Second, "how often --repo is checked for others' changes")
	debug := fl.Bool("debug", false, "debug logging")
	if err := fl.Parse(args); err != nil {
		return err
	}
	var err error
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var backend boards.Backend = boards.DirBackend{Root: *data}
	var gb *gitbackend.Backend
	if *repoFlag != "" {
		gb, err = openGit(ctx, *repoFlag, *branch, *data, *fetchEvery, log)
		if err != nil {
			return err
		}
		backend = gb
	}
	store, err := boards.Open(ctx, backend)
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

	syncCtx, stopSync := context.WithCancel(context.Background())
	defer stopSync()
	syncDone := make(chan struct{})
	if gb != nil {
		gb.OnStatus = srv.NotifyStatus
		gb.OnRemote = func(ctx context.Context, remotes []boards.Remote) {
			gained, err := store.MergeRemote(remotes)
			if err != nil {
				log.Error("merge remote boards", "err", err)
			}
			srv.ApplyRemote(ctx, gained)
			srv.NotifyStatus()
		}
		remote := *repoFlag
		srv.SetBackendStatus(func() server.SyncStatus {
			st := gb.Status()
			out := server.SyncStatus{Backend: "git", Remote: remote, Unpushed: st.Unpushed}
			if !st.LastSync.IsZero() {
				out.LastSync = &st.LastSync
			}
			if !st.UnpushedSince.IsZero() {
				out.UnpushedSince = &st.UnpushedSince
			}
			if st.Err != nil {
				out.Error = st.Err.Error()
			}
			return out
		})
		go func() {
			defer close(syncDone)
			gb.Run(syncCtx, 30*time.Second)
		}()
	} else {
		close(syncDone)
	}

	flushCtx, stopFlusher := context.WithCancel(context.Background())
	defer stopFlusher()
	flusherDone := make(chan struct{})
	go func() {
		defer close(flusherDone)
		store.RunFlusher(flushCtx, boards.DefaultFlushPolicy, 30*time.Second, func(err error) {
			log.Error("write boards", "err", err)
			srv.NotifyStatus()
		})
	}()

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
	// Rooms first (ygo flushes their coalesced updates into the store), then
	// the store's final write to the backend, then HTTP.
	if err := srv.Shutdown(sctx); err != nil {
		log.Error("sync shutdown", "err", err)
	}
	stopFlusher()
	<-flusherDone
	stopSync() // after the final flush, so its commits get the last push
	<-syncDone
	if err := httpSrv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openGit opens (or clones) the board repository into <data>/repo.git.
func openGit(ctx context.Context, repo, branch, data string, fetchEvery time.Duration, log *slog.Logger) (*gitbackend.Backend, error) {
	remote := gitstore.Remote{URL: repo}
	switch {
	case strings.HasPrefix(repo, "https://") || strings.HasPrefix(repo, "http://"):
		tok := os.Getenv("AEBOARD_GIT_TOKEN")
		if tok == "" {
			var err error
			if tok, err = ghcli.NewTokenSource().Token(ctx); err != nil {
				return nil, fmt.Errorf("--repo %s needs a token: set AEBOARD_GIT_TOKEN or log in with gh: %w", repo, err)
			}
		}
		remote.Auth = &githttp.BasicAuth{Username: "x-access-token", Password: tok}
	case strings.Contains(repo, "://") || strings.HasPrefix(repo, "git@"):
		// ssh and other transports: go-git's defaults (ssh-agent).
	default:
		abs, err := filepath.Abs(repo)
		if err != nil {
			return nil, err
		}
		remote.URL = "file://" + abs
	}
	r, created, err := gitstore.OpenDir(ctx, filepath.Join(data, "repo.git"), remote, gitstore.Options{
		Committer: gitstore.Identity{Name: "aeboard", Email: "aeboard@users.noreply.github.com"},
		Branch:    plumbing.NewBranchReferenceName(branch),
	})
	if err != nil {
		return nil, fmt.Errorf("open board repository %s: %w", repo, err)
	}
	log.Info("board repository", "repo", repo, "branch", branch, "new", created)
	return gitbackend.New(r, &remote, gitbackend.Options{Log: log, FetchInterval: fetchEvery}), nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
