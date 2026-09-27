package testutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/siddharth-732/high-concurrency-ticket-booking-system/internal/invariant"
)

// AssertInvariants fails the test if the database violates any known
// data-integrity rule (see internal/invariant). Call it at the end of any
// test that exercises concurrent or multi-step booking logic: it is the
// generic backstop behind whatever specific assertions that test already
// makes, and it is what will catch a regression introduced by a later
// task (e.g. Task 8's sweeper forgetting to release a seat) even if that
// task's own tests don't happen to check for it.
func AssertInvariants(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	violations, err := invariant.Check(ctx, pool)
	if err != nil {
		t.Fatalf("run invariant check: %v", err)
	}
	for _, v := range violations {
		t.Errorf("invariant violated: %s", v)
	}
}
