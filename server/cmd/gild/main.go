package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	_ "modernc.org/sqlite"
	"google.golang.org/grpc"

	"github.com/jedutools/gil/core/provider"
	"github.com/jedutools/gil/core/session"
	gilv1 "github.com/jedutools/gil/proto/gen/gil/v1"
	"github.com/jedutools/gil/server/internal/service"
	"github.com/jedutools/gil/server/internal/uds"
)

// server wraps a gRPC server, Unix Domain Socket listener, and SQLite database,
// providing unified startup and graceful shutdown for the gild daemon.
type server struct {
	grpc *grpc.Server
	lis  net.Listener
	db   *sql.DB
}

// newServer creates and initializes a gRPC server listening on sockPath with a SQLite
// database at dbPath. It returns an error if the database cannot be opened, migrations
// fail, the socket cannot be created, or gRPC registration fails. Partial failures
// are cleaned up: if migration or socket creation fails, the database is closed.
func newServer(dbPath, sockPath, sessionsBase string) (*server, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if err := session.Migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	lis, err := uds.Listen(sockPath)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	// Factory provides mock or anthropic provider based on name
	factory := func(name string) (provider.Provider, string, error) {
		switch name {
		case "mock":
			return provider.NewMock([]string{
				`{"domain":"unknown","domain_confidence":0.5,"tech_hints":[],"scale_hint":"unknown","ambiguity":"none"}`,
				"What's your project goal?",
			}), "mock-model", nil
		case "anthropic", "":
			key := os.Getenv("ANTHROPIC_API_KEY")
			if key == "" {
				return nil, "", fmt.Errorf("ANTHROPIC_API_KEY not set")
			}
			return provider.NewAnthropic(key), "claude-opus-4-7", nil
		default:
			return nil, "", fmt.Errorf("unknown provider %q", name)
		}
	}

	g := grpc.NewServer()
	repo := session.NewRepo(db)
	gilv1.RegisterSessionServiceServer(g, service.NewSessionService(repo))
	gilv1.RegisterInterviewServiceServer(g, service.NewInterviewService(repo, sessionsBase, factory))
	gilv1.RegisterRunServiceServer(g, service.NewRunService(repo, sessionsBase, factory))

	return &server{grpc: g, lis: lis, db: db}, nil
}

// Serve starts the gRPC server on the underlying Unix Domain Socket listener.
// It blocks until the listener is closed or an error occurs.
func (s *server) Serve() error {
	return s.grpc.Serve(s.lis)
}

// Stop gracefully stops the gRPC server, closes the listener, and closes the database.
// It is safe to call multiple times.
func (s *server) Stop() {
	s.grpc.GracefulStop()
	_ = s.lis.Close()
	_ = s.db.Close()
}

func main() {
	home, _ := os.UserHomeDir()
	defaultBase := filepath.Join(home, ".gil")
	base := flag.String("base", defaultBase, "data directory")
	foreground := flag.Bool("foreground", false, "run in foreground")
	flag.Parse()

	if !*foreground {
		fmt.Fprintln(os.Stderr, "gild: --foreground required for now (detach mode in Phase 2)")
		os.Exit(2)
	}

	if err := os.MkdirAll(*base, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "gild:", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(*base, "sessions.db")
	sockPath := filepath.Join(*base, "gild.sock")
	sessionsBase := filepath.Join(*base, "sessions")

	srv, err := newServer(dbPath, sockPath, sessionsBase)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gild:", err)
		os.Exit(1)
	}

	slog.Info("gild ready", "socket", sockPath, "db", dbPath)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- err
		}
	}()

	select {
	case <-stop:
		slog.Info("gild shutting down")
	case err := <-errCh:
		fmt.Fprintln(os.Stderr, "gild serve:", err)
	}
	srv.Stop()
}
