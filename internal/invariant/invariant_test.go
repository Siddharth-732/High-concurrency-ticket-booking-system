package invariant_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/siddharth-732/high-concurrency-ticket-booking-system/internal/booking"
	"github.com/siddharth-732/high-concurrency-ticket-booking-system/internal/invariant"
	"github.com/siddharth-732/high-concurrency-ticket-booking-system/internal/testutil"
)

func TestCheck_CleanDatabaseHasNoViolations(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	var showID uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO shows (name, venue, starts_at) VALUES ('Show', 'Venue', now() + interval '7 days')
		RETURNING id
	`).Scan(&showID)
	if err != nil {
		t.Fatalf("seed show: %v", err)
	}

	var seatID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO seats (show_id, label, price_cents) VALUES ($1, 'A1', 1000)
		RETURNING id
	`, showID).Scan(&seatID)
	if err != nil {
		t.Fatalf("seed seat: %v", err)
	}

	if _, err := store.CreateHold(ctx, showID, uuid.New(), []uuid.UUID{seatID}, uuid.NewString()); err != nil {
		t.Fatalf("CreateHold: %v", err)
	}

	testutil.AssertInvariants(t, ctx, pool)
}

// TestCheck_DetectsClaimOnReleasedBooking proves the checker actually
// catches something, rather than always reporting "clean". It simulates
// the exact bug the rule exists for: a future release code path (Task 8's
// sweeper, or CancelBooking) that updates a booking's status but forgets
// to delete its booking_seats rows. We manufacture that bug directly with
// raw SQL, bypassing the Store entirely, since no correct code path in
// this package would ever produce it.
func TestCheck_DetectsClaimOnReleasedBooking(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	var showID uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO shows (name, venue, starts_at) VALUES ('Show', 'Venue', now() + interval '7 days')
		RETURNING id
	`).Scan(&showID)
	if err != nil {
		t.Fatalf("seed show: %v", err)
	}

	var seatID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO seats (show_id, label, price_cents) VALUES ($1, 'A1', 1000)
		RETURNING id
	`, showID).Scan(&seatID)
	if err != nil {
		t.Fatalf("seed seat: %v", err)
	}

	b, err := store.CreateHold(ctx, showID, uuid.New(), []uuid.UUID{seatID}, uuid.NewString())
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}

	// Simulate the bug: mark the booking released without deleting its
	// booking_seats row.
	if _, err := pool.Exec(ctx, `UPDATE bookings SET status = 'cancelled' WHERE id = $1`, b.ID); err != nil {
		t.Fatalf("simulate bug: %v", err)
	}

	violations, err := invariant.Check(ctx, pool)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	found := false
	for _, v := range violations {
		if v.Rule == "claim_on_released_booking" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a claim_on_released_booking violation, got %v", violations)
	}
}

// TestCheck_DetectsSeatShowMismatch manufactures the other kind of bug the
// checker guards against: a booking_seats row linking a booking to a seat
// from a different show than the one the booking is for. Nothing in
// Store.CreateHold can produce this -- it always inserts seats and their
// parent booking for the same show_id -- so, as with the test above, we
// reach past the Store and insert the bad row directly.
func TestCheck_DetectsSeatShowMismatch(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	var show1, show2 uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO shows (name, venue, starts_at) VALUES ('Show 1', 'Venue', now() + interval '7 days') RETURNING id
	`).Scan(&show1); err != nil {
		t.Fatalf("seed show1: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO shows (name, venue, starts_at) VALUES ('Show 2', 'Venue', now() + interval '7 days') RETURNING id
	`).Scan(&show2); err != nil {
		t.Fatalf("seed show2: %v", err)
	}

	var seat1, seat2 uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO seats (show_id, label, price_cents) VALUES ($1, 'A1', 1000) RETURNING id
	`, show1).Scan(&seat1); err != nil {
		t.Fatalf("seed seat1: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO seats (show_id, label, price_cents) VALUES ($1, 'B1', 1000) RETURNING id
	`, show2).Scan(&seat2); err != nil {
		t.Fatalf("seed seat2: %v", err)
	}

	b, err := store.CreateHold(ctx, show1, uuid.New(), []uuid.UUID{seat1}, uuid.NewString())
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}

	// Simulate the bug: wire show2's seat onto show1's booking directly.
	if _, err := pool.Exec(ctx, `INSERT INTO booking_seats (booking_id, seat_id) VALUES ($1, $2)`, b.ID, seat2); err != nil {
		t.Fatalf("simulate bug: %v", err)
	}

	violations, err := invariant.Check(ctx, pool)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	found := false
	for _, v := range violations {
		if v.Rule == "seat_show_mismatch" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a seat_show_mismatch violation, got %v", violations)
	}
}
