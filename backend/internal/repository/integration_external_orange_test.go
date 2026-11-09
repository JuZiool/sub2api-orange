//go:build integration

package repository

import (
	"context"
	"database/sql"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	redisclient "github.com/redis/go-redis/v9"
)

// runOrangeExternalRepositoryTests is an explicit alternative for local WSLC
// test databases. The default upstream Docker harness does not call this path.
// A new, empty database is required for every run; this helper never clears data.
func runOrangeExternalRepositoryTests(ctx context.Context, m *testing.M, dsn string) int {
	db, err := openSQLWithRetry(ctx, dsn, 30*time.Second)
	if err != nil {
		// Connection errors can contain DSN credentials, so do not log err here.
		log.Print("Orange external repository tests: database connection failed")
		return 1
	}
	defer func() { _ = db.Close() }()

	if !orangeRepositoryDatabaseIsEmpty(ctx, db) {
		return 1
	}
	if err := ApplyMigrations(ctx, db); err != nil {
		log.Printf("Orange external repository tests: migrations failed: %v", err)
		return 1
	}
	log.Print("Orange external repository tests: migrations applied to isolated empty database")

	integrationDB = db
	integrationEntClient = dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	defer func() { _ = integrationEntClient.Close() }()

	if rawURL := strings.TrimSpace(os.Getenv("ORANGE_REPOSITORY_TEST_REDIS_URL")); rawURL != "" {
		opts, err := redisclient.ParseURL(rawURL)
		if err != nil {
			log.Print("Orange external repository tests: invalid Redis URL")
			return 1
		}
		integrationRedis = redisclient.NewClient(opts)
		defer func() { _ = integrationRedis.Close() }()
		if err := integrationRedis.Ping(ctx).Err(); err != nil {
			log.Print("Orange external repository tests: Redis connection failed")
			return 1
		}
	}

	return m.Run()
}

func orangeRepositoryDatabaseIsEmpty(ctx context.Context, db *sql.DB) bool {
	var name string
	if err := db.QueryRowContext(ctx, "SELECT current_database()").Scan(&name); err != nil {
		log.Print("Orange external repository tests: cannot verify database name")
		return false
	}
	if name != "orange_repository_test" && !strings.HasPrefix(name, "orange_repository_test_") {
		log.Print("Orange external repository tests: refusing database without orange_repository_test name prefix")
		return false
	}
	var relations int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM pg_class AS c
		JOIN pg_namespace AS n ON n.oid = c.relnamespace
		WHERE n.nspname <> 'information_schema'
		  AND n.nspname NOT LIKE 'pg_%'
	`).Scan(&relations); err != nil {
		log.Print("Orange external repository tests: cannot verify database is empty")
		return false
	}
	if relations != 0 {
		log.Print("Orange external repository tests: refusing nonempty database; create a fresh isolated test database")
		return false
	}
	return true
}
