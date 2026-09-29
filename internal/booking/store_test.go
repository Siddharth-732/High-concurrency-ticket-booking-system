package booking_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/siddharth-732/high-concurrency-ticket-booking-system/internal/booking"
	"github.com/siddharth-732/high-concurrency-ticket-booking-system/internal/testutil"
)

func seedShowAndSeats(t *testing.T, ctx context.Context, pool *pgxpool.Pool, n int) (showID uuid.UUID, seatIDs []uuid.UUID) {
	t.Helper()

	err := pool.QueryRow(ctx, `
		INSERT INTO shows (name, venue, starts_at) VALUES ('Test Show', 'Test Venue', now() + interval '7 days')
		RETURNING id
	`).Scan(&showID)
	if err != nil {
		t.Fatalf("seed show: %v", err)
	}

	for i := 0; i < n; i++ {
		var seatID uuid.UUID
		err := pool.QueryRow(ctx, `
			INSERT INTO seats (show_id, label, price_cents) VALUES ($1, $2, 1000)
			RETURNING id
		`, showID, uuid.NewString()).Scan(&seatID)
		if err != nil {
			t.Fatalf("seed seat: %v", err)
		}
		seatIDs = append(seatIDs, seatID)
	}

	return showID, seatIDs
}

func TestCreateHold_Success(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 2)
	userID := uuid.New()

	b, err := store.CreateHold(ctx, showID, userID, seatIDs, "idem-key-1")
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}

	if b.Status != booking.StatusHeld {
		t.Errorf("status = %q, want %q", b.Status, booking.StatusHeld)
	}
	if len(b.SeatIDs) != 2 {
		t.Errorf("len(SeatIDs) = %d, want 2", len(b.SeatIDs))
	}

	// The rows the invariant depends on must actually exist.
	var count int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM booking_seats WHERE booking_id = $1`, b.ID).Scan(&count)
	if err != nil {
		t.Fatalf("count booking_seats: %v", err)
	}
	if count != 2 {
		t.Errorf("booking_seats rows = %d, want 2", count)
	}
}

func TestCreateHold_SeatAlreadyHeld(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 1)
	contestedSeat := seatIDs[0]

	firstUser := uuid.New()
	first, err := store.CreateHold(ctx, showID, firstUser, []uuid.UUID{contestedSeat}, "idem-key-first")
	if err != nil {
		t.Fatalf("first CreateHold: %v", err)
	}

	secondUser := uuid.New()
	_, err = store.CreateHold(ctx, showID, secondUser, []uuid.UUID{contestedSeat}, "idem-key-second")
	if !errors.Is(err, booking.ErrSeatUnavailable) {
		t.Fatalf("second CreateHold error = %v, want ErrSeatUnavailable", err)
	}

	var bookingID uuid.UUID
	err = pool.QueryRow(ctx, `SELECT booking_id FROM booking_seats WHERE seat_id = $1`, contestedSeat).Scan(&bookingID)
	if err != nil {
		t.Fatalf("query booking_seats: %v", err)
	}
	if bookingID != first.ID {
		t.Errorf("seat belongs to booking %s, want %s", bookingID, first.ID)
	}

	var secondUserBookingCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM bookings WHERE user_id = $1`, secondUser).Scan(&secondUserBookingCount)
	if err != nil {
		t.Fatalf("count second user's bookings: %v", err)
	}
	if secondUserBookingCount != 0 {
		t.Errorf("second user has %d booking rows, want 0 (transaction should have rolled back)", secondUserBookingCount)
	}
}

func TestCreateHold_NoSeats(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, _ := seedShowAndSeats(t, ctx, pool, 0)

	_, err := store.CreateHold(ctx, showID, uuid.New(), nil, "idem-key")
	if !errors.Is(err, booking.ErrNoSeats) {
		t.Fatalf("err = %v, want ErrNoSeats", err)
	}
}

// TestCreateHold_IdempotentRetry_ReturnsOriginalBooking is the behavior
// Task 7 exists for: a caller retries the exact same request (same key,
// same show, same seats) -- perhaps because they never saw the first
// response -- and must get back the original booking, not an error and
// not a second booking.
func TestCreateHold_IdempotentRetry_ReturnsOriginalBooking(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 2)
	userID := uuid.New()

	first, err := store.CreateHold(ctx, showID, userID, seatIDs, "retry-key")
	if err != nil {
		t.Fatalf("first CreateHold: %v", err)
	}

	second, err := store.CreateHold(ctx, showID, userID, seatIDs, "retry-key")
	if err != nil {
		t.Fatalf("retry CreateHold: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("retry returned booking %s, want the original %s", second.ID, first.ID)
	}

	var bookingCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM bookings WHERE user_id = $1`, userID).Scan(&bookingCount)
	if err != nil {
		t.Fatalf("count bookings: %v", err)
	}
	if bookingCount != 1 {
		t.Errorf("bookings for user = %d, want 1 (retry must not create a second booking)", bookingCount)
	}

	testutil.AssertInvariants(t, ctx, pool)
}

// TestCreateHold_IdempotencyKeyReusedForDifferentRequest covers the
// dangerous case a naive idempotency implementation gets wrong: the same
// key reused for a request with *different* seats. This must not
// silently return the original booking (the caller would wrongly believe
// their new seats were held) -- it must be rejected as a distinct error.
func TestCreateHold_IdempotencyKeyReusedForDifferentRequest(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 2)
	userID := uuid.New()

	_, err := store.CreateHold(ctx, showID, userID, seatIDs[:1], "same-key")
	if err != nil {
		t.Fatalf("first CreateHold: %v", err)
	}

	_, err = store.CreateHold(ctx, showID, userID, seatIDs[1:], "same-key")
	if !errors.Is(err, booking.ErrIdempotencyKeyReused) {
		t.Fatalf("second CreateHold error = %v, want ErrIdempotencyKeyReused", err)
	}
}
