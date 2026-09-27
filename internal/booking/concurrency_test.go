package booking_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/siddharth-732/high-concurrency-ticket-booking-system/internal/booking"
	"github.com/siddharth-732/high-concurrency-ticket-booking-system/internal/testutil"
)

// result is one goroutine's outcome, collected after the race so the
// counting itself happens single-threaded -- the race detector is there
// to catch us if CreateHold or pgxpool ever touch shared state unsafely,
// not to catch bugs in how the test tallies results.
type result struct {
	err error
}

// TestConcurrentHold_ExactlyOneWinner is the test the whole project's
// headline claim rests on: many goroutines fight for the same single
// seat at the same instant, and exactly one of them must win.
func TestConcurrentHold_ExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 1)
	contestedSeat := seatIDs[0]

	const attackers = 300
	start := make(chan struct{}) // closed once, releases every goroutine at the same instant
	results := make([]result, attackers)

	var wg sync.WaitGroup
	wg.Add(attackers)
	for i := 0; i < attackers; i++ {
		go func(i int) {
			defer wg.Done()
			<-start // wait here until every goroutine is queued up and ready
			_, err := store.CreateHold(ctx, showID, uuid.New(), []uuid.UUID{contestedSeat}, uuid.NewString())
			results[i] = result{err: err}
		}(i)
	}
	close(start) // fire the starting gun
	wg.Wait()

	var wins, losses, unexpected int
	for _, r := range results {
		switch {
		case r.err == nil:
			wins++
		case errors.Is(r.err, booking.ErrSeatUnavailable):
			losses++
		default:
			unexpected++
			t.Logf("unexpected error: %v", r.err)
		}
	}

	if wins != 1 {
		t.Errorf("wins = %d, want exactly 1", wins)
	}
	if losses != attackers-1 {
		t.Errorf("losses = %d, want %d", losses, attackers-1)
	}
	if unexpected != 0 {
		t.Errorf("unexpected errors = %d, want 0", unexpected)
	}

	// Don't just trust the in-memory tally -- ask the database directly,
	// which is the actual source of truth the invariant depends on.
	var seatRowCount int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM booking_seats WHERE seat_id = $1`, contestedSeat).Scan(&seatRowCount)
	if err != nil {
		t.Fatalf("count booking_seats: %v", err)
	}
	if seatRowCount != 1 {
		t.Errorf("booking_seats rows for contested seat = %d, want 1", seatRowCount)
	}
}

// TestConcurrentHold_OverlappingMultiSeatAllOrNothing goes one step
// further than a single contested seat: two different multi-seat
// requests that overlap on one seat race each other. Only one of them
// may win, and a loser must never end up holding even the seat it didn't
// share with the winner -- that is the "all or nothing" guarantee under
// real contention, not just in a single-threaded test.
func TestConcurrentHold_OverlappingMultiSeatAllOrNothing(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 3)
	seatA, seatB, seatC := seatIDs[0], seatIDs[1], seatIDs[2]

	// Half the attackers want {A, B}, half want {B, C}. Every request
	// shares seat B with every request from the other group, so at most
	// one request across the whole run can succeed.
	const attackersPerGroup = 100
	type attempt struct {
		seats []uuid.UUID
	}
	var attempts []attempt
	for i := 0; i < attackersPerGroup; i++ {
		attempts = append(attempts,
			attempt{seats: []uuid.UUID{seatA, seatB}},
			attempt{seats: []uuid.UUID{seatB, seatC}},
		)
	}

	start := make(chan struct{})
	results := make([]result, len(attempts))

	var wg sync.WaitGroup
	wg.Add(len(attempts))
	for i, a := range attempts {
		go func(i int, seats []uuid.UUID) {
			defer wg.Done()
			<-start
			_, err := store.CreateHold(ctx, showID, uuid.New(), seats, uuid.NewString())
			results[i] = result{err: err}
		}(i, a.seats)
	}
	close(start)
	wg.Wait()

	var wins, losses, unexpected int
	for _, r := range results {
		switch {
		case r.err == nil:
			wins++
		case errors.Is(r.err, booking.ErrSeatUnavailable):
			losses++
		default:
			unexpected++
			t.Logf("unexpected error: %v", r.err)
		}
	}

	if wins != 1 {
		t.Errorf("wins = %d, want exactly 1", wins)
	}
	if unexpected != 0 {
		t.Errorf("unexpected errors = %d, want 0", unexpected)
	}
	if wins+losses != len(attempts) {
		t.Errorf("wins+losses = %d, want %d", wins+losses, len(attempts))
	}

	// The database must show exactly 2 claimed seats total -- whichever
	// pair won -- never 1 (a partial hold) and never 4 (both pairs
	// somehow succeeding).
	var claimedCount int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FROM booking_seats WHERE seat_id = ANY($1)
	`, []uuid.UUID{seatA, seatB, seatC}).Scan(&claimedCount)
	if err != nil {
		t.Fatalf("count claimed seats: %v", err)
	}
	if claimedCount != 2 {
		t.Errorf("claimed seats = %d, want exactly 2", claimedCount)
	}
}
