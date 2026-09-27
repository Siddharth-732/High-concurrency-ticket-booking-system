// Package testutil provides a disposable Postgres instance for tests that
// need to run real SQL, not a mock. It depends on the "testing" package, so
// it must only ever be imported from _test.go files -- if a non-test
// package imported it, "testing" would end up compiled into that binary.
package testutil

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// NewPool starts a fresh, disposable Postgres 16 container, applies every
// migration in migrations/ against it, and returns a connection pool.
//
// We spin up a brand new container per test run instead of pointing at the
// docker-compose Postgres from Task 2 for two reasons: tests must be
// hermetic (no leftover rows from a previous run affecting this one), and
// they must be runnable with nothing more than `go test` -- no requirement
// to remember `docker compose up` first. This is what CI will do too.
func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("booking"),
		tcpostgres.WithUsername("booking"),
		tcpostgres.WithPassword("booking"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres container: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("build connection string: %v", err)
	}

	applyMigrations(t, ctx, connStr)

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("connect pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// applyMigrations replays every migrations/*.up.sql file, in order,
// against the freshly created database.
func applyMigrations(t *testing.T, ctx context.Context, connStr string) {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		t.Fatalf("parse config for migrations: %v", err)
	}
	// A migration file is several semicolon-separated statements. pgx's
	// default (extended) query protocol only allows one statement per
	// Exec call, so we switch to the simple protocol -- the same one
	// psql uses -- just for this one-off setup connection. The pool the
	// Store actually uses in CreateHold stays on the default protocol.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	setupPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect for migrations: %v", err)
	}
	defer setupPool.Close()

	dir, err := filepath.Abs("../../migrations")
	if err != nil {
		t.Fatalf("resolve migrations dir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir %s: %v", dir, err)
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files) // filenames are zero-padded, e.g. 000001_..., so lexical order is migration order

	for _, name := range files {
		sqlBytes, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if _, err := setupPool.Exec(ctx, string(sqlBytes)); err != nil {
			t.Fatalf("apply migration %s: %v", name, err)
		}
	}
}
