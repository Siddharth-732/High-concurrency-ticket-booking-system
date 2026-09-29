package booking_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/siddharth-732/high-concurrency-ticket-booking-system/internal/booking"
	"github.com/siddharth-732/high-concurrency-ticket-booking-system/internal/testutil"
)

// seedHeldBooking inserts a booking and its seats directly with a
// caller-chosen expires_at, bypassing Store.CreateHold entirely. That
// function always sets expires_at to now()+HoldTTL (5 minutes in the
// future), so there is no way to use it to create an *already expired*
// hold for a test -- and a real test cannot wait 5 real minutes. Seeding
// a past expires_at directly is how we control time here instead.
func seedHeldBooking(t *testing.T, ctx context.Context, pool *pgxpool.Pool, showID uuid.UUID, seatIDs []uuid.UUID, expiresAt time.Time) uuid.UUID {
	t.Helper()

	var bookingID uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO bookings (show_id, user_id, status, idempotency_key, request_fingerprint, expires_at)
		VALUES ($1, $2, 'held', $3, $4, $5)
		RETURNING id
	`, showID, uuid.New(), uuid.NewString(), uuid.NewString(), expiresAt).Scan(&bookingID)
	if err != nil {
		t.Fatalf("seed booking: %v", err)
	}

	for _, seatID := range seatIDs {
		_, err := pool.Exec(ctx, `INSERT INTO booking_seats (booking_id, seat_id) VALUES ($1, $2)`, bookingID, seatID)
		if err != nil {
			t.Fatalf("seed booking_seats: %v", err)
		}
	}

	return bookingID
}

func TestSweepExpiredHolds_ReleasesExpiredHold(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 2)
	bookingID := seedHeldBooking(t, ctx, pool, showID, seatIDs, time.Now().Add(-time.Minute))

	released, err := store.SweepExpiredHolds(ctx)
	if err != nil {
		t.Fatalf("SweepExpiredHolds: %v", err)
	}
	if released != 1 {
		t.Errorf("released = %d, want 1", released)
	}

	var status string
	err = pool.QueryRow(ctx, `SELECT status FROM bookings WHERE id = $1`, bookingID).Scan(&status)
	if err != nil {
		t.Fatalf("query booking status: %v", err)
	}
	if status != string(booking.StatusExpired) {
		t.Errorf("status = %q, want %q", status, booking.StatusExpired)
	}

	var seatCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM booking_seats WHERE booking_id = $1`, bookingID).Scan(&seatCount)
	if err != nil {
		t.Fatalf("count booking_seats: %v", err)
	}
	if seatCount != 0 {
		t.Errorf("booking_seats rows = %d, want 0 (seats should be released)", seatCount)
	}

	testutil.AssertInvariants(t, ctx, pool)
}

func TestSweepExpiredHolds_LeavesUnexpiredHoldsAlone(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 1)
	bookingID := seedHeldBooking(t, ctx, pool, showID, seatIDs, time.Now().Add(booking.HoldTTL))

	released, err := store.SweepExpiredHolds(ctx)
	if err != nil {
		t.Fatalf("SweepExpiredHolds: %v", err)
	}
	if released != 0 {
		t.Errorf("released = %d, want 0 (hold has not expired yet)", released)
	}

	var status string
	err = pool.QueryRow(ctx, `SELECT status FROM bookings WHERE id = $1`, bookingID).Scan(&status)
	if err != nil {
		t.Fatalf("query booking status: %v", err)
	}
	if status != string(booking.StatusHeld) {
		t.Errorf("status = %q, want %q (untouched)", status, booking.StatusHeld)
	}
}

// TestSweepExpiredHolds_DoesNotRaceWithConcurrentConfirm proves the
// locking contract that Task 9's real ConfirmBooking must follow: the
// sweeper and a concurrent state-changing transaction on the same
// booking must never both "win". Task 9's ConfirmBooking does not exist
// yet, so this test stands in for it with a minimal hand-rolled
// transaction that follows the same pattern (SELECT ... FOR UPDATE, then
// update). It deliberately does not re-check expires_at itself -- that
// business decision (should a very-late payment ever be honored?) is
// Task 9's to make. This test only cares about one thing: whatever the
// outcome, it must be *consistent* -- never seats retained on an expired
// booking, and never seats missing on a confirmed one.
func TestSweepExpiredHolds_DoesNotRaceWithConcurrentConfirm(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 2)
	bookingID := seedHeldBooking(t, ctx, pool, showID, seatIDs, time.Now().Add(-time.Minute))

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		<-start
		if _, err := store.SweepExpiredHolds(ctx); err != nil {
			t.Errorf("SweepExpiredHolds: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		<-start
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Errorf("begin confirm tx: %v", err)
			return
		}
		defer tx.Rollback(ctx)

		var status string
		err = tx.QueryRow(ctx, `SELECT status FROM bookings WHERE id = $1 FOR UPDATE`, bookingID).Scan(&status)
		if err != nil {
			t.Errorf("lock booking for confirm: %v", err)
			return
		}
		if status != string(booking.StatusHeld) {
			// The sweeper got here first and already moved it on -- a
			// correct outcome, nothing to do.
			return
		}
		if _, err := tx.Exec(ctx, `UPDATE bookings SET status = 'confirmed', updated_at = now() WHERE id = $1`, bookingID); err != nil {
			t.Errorf("confirm booking: %v", err)
			return
		}
		if err := tx.Commit(ctx); err != nil {
			t.Errorf("commit confirm: %v", err)
		}
	}()

	close(start)
	wg.Wait()

	var status string
	err := pool.QueryRow(ctx, `SELECT status FROM bookings WHERE id = $1`, bookingID).Scan(&status)
	if err != nil {
		t.Fatalf("query final status: %v", err)
	}

	var seatCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM booking_seats WHERE booking_id = $1`, bookingID).Scan(&seatCount)
	if err != nil {
		t.Fatalf("count booking_seats: %v", err)
	}

	switch status {
	case string(booking.StatusExpired):
		if seatCount != 0 {
			t.Errorf("status = expired but booking_seats rows = %d, want 0", seatCount)
		}
	case string(booking.StatusConfirmed):
		if seatCount != len(seatIDs) {
			t.Errorf("status = confirmed but booking_seats rows = %d, want %d", seatCount, len(seatIDs))
		}
	default:
		t.Errorf("final status = %q, want %q or %q", status, booking.StatusExpired, booking.StatusConfirmed)
	}

	testutil.AssertInvariants(t, ctx, pool)
}
