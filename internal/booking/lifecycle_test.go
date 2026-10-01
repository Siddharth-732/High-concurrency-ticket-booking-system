package booking_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/siddharth-732/high-concurrency-ticket-booking-system/internal/booking"
	"github.com/siddharth-732/high-concurrency-ticket-booking-system/internal/testutil"
)

func TestCheckout_Success(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 2)
	held, err := store.CreateHold(ctx, showID, uuid.New(), seatIDs, uuid.NewString())
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}

	paymentID := uuid.NewString()
	b, err := store.Checkout(ctx, held.ID, paymentID)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if b.Status != booking.StatusAwaitingPayment {
		t.Errorf("status = %q, want %q", b.Status, booking.StatusAwaitingPayment)
	}

	var amountCents int64
	var paymentStatus string
	err = pool.QueryRow(ctx, `SELECT amount_cents, status FROM payments WHERE external_payment_id = $1`, paymentID).
		Scan(&amountCents, &paymentStatus)
	if err != nil {
		t.Fatalf("query payment: %v", err)
	}
	if amountCents != 2000 { // two seats seeded at 1000 cents each in seedShowAndSeats
		t.Errorf("amount_cents = %d, want 2000", amountCents)
	}
	if paymentStatus != "pending" {
		t.Errorf("payment status = %q, want pending", paymentStatus)
	}
}

func TestCheckout_FailsIfNotHeld(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 1)
	held, err := store.CreateHold(ctx, showID, uuid.New(), seatIDs, uuid.NewString())
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}
	if _, err := store.Checkout(ctx, held.ID, uuid.NewString()); err != nil {
		t.Fatalf("first Checkout: %v", err)
	}

	_, err = store.Checkout(ctx, held.ID, uuid.NewString())
	if !errors.Is(err, booking.ErrBookingNotHeld) {
		t.Fatalf("second Checkout error = %v, want ErrBookingNotHeld", err)
	}
}

func TestCheckout_FailsIfExpired(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 1)
	bookingID := seedHeldBooking(t, ctx, pool, showID, seatIDs, time.Now().Add(-time.Minute))

	_, err := store.Checkout(ctx, bookingID, uuid.NewString())
	if !errors.Is(err, booking.ErrHoldExpired) {
		t.Fatalf("Checkout error = %v, want ErrHoldExpired", err)
	}
}

func TestHandlePaymentCallback_Success(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 2)
	held, err := store.CreateHold(ctx, showID, uuid.New(), seatIDs, uuid.NewString())
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}
	paymentID := uuid.NewString()
	if _, err := store.Checkout(ctx, held.ID, paymentID); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	b, err := store.HandlePaymentCallback(ctx, paymentID, true)
	if err != nil {
		t.Fatalf("HandlePaymentCallback: %v", err)
	}
	if b.Status != booking.StatusConfirmed {
		t.Errorf("status = %q, want %q", b.Status, booking.StatusConfirmed)
	}

	var seatCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM booking_seats WHERE booking_id = $1`, held.ID).Scan(&seatCount)
	if err != nil {
		t.Fatalf("count booking_seats: %v", err)
	}
	if seatCount != 2 {
		t.Errorf("booking_seats rows = %d, want 2 (confirmed booking keeps its seats)", seatCount)
	}

	testutil.AssertInvariants(t, ctx, pool)
}

func TestHandlePaymentCallback_Failure(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 2)
	held, err := store.CreateHold(ctx, showID, uuid.New(), seatIDs, uuid.NewString())
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}
	paymentID := uuid.NewString()
	if _, err := store.Checkout(ctx, held.ID, paymentID); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	b, err := store.HandlePaymentCallback(ctx, paymentID, false)
	if err != nil {
		t.Fatalf("HandlePaymentCallback: %v", err)
	}
	if b.Status != booking.StatusFailed {
		t.Errorf("status = %q, want %q", b.Status, booking.StatusFailed)
	}

	var seatCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM booking_seats WHERE booking_id = $1`, held.ID).Scan(&seatCount)
	if err != nil {
		t.Fatalf("count booking_seats: %v", err)
	}
	if seatCount != 0 {
		t.Errorf("booking_seats rows = %d, want 0 (failed payment must free the seats)", seatCount)
	}

	testutil.AssertInvariants(t, ctx, pool)
}

// TestHandlePaymentCallback_FirstCallbackWins covers the project's
// "duplicated payment callback" claim directly: payment-svc's fake
// gateway deliberately sends some callbacks twice. A later duplicate
// must never override the outcome the first callback already applied --
// not even if the duplicate claims a different outcome than the first.
func TestHandlePaymentCallback_FirstCallbackWins(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 1)
	held, err := store.CreateHold(ctx, showID, uuid.New(), seatIDs, uuid.NewString())
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}
	paymentID := uuid.NewString()
	if _, err := store.Checkout(ctx, held.ID, paymentID); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	first, err := store.HandlePaymentCallback(ctx, paymentID, true)
	if err != nil {
		t.Fatalf("first HandlePaymentCallback: %v", err)
	}
	if first.Status != booking.StatusConfirmed {
		t.Fatalf("first callback status = %q, want confirmed", first.Status)
	}

	// A duplicate, conflicting callback arrives late and claims failure.
	second, err := store.HandlePaymentCallback(ctx, paymentID, false)
	if err != nil {
		t.Fatalf("duplicate HandlePaymentCallback: %v", err)
	}
	if second.Status != booking.StatusConfirmed {
		t.Errorf("status after duplicate = %q, want still %q (first callback must win)", second.Status, booking.StatusConfirmed)
	}

	var seatCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM booking_seats WHERE booking_id = $1`, held.ID).Scan(&seatCount)
	if err != nil {
		t.Fatalf("count booking_seats: %v", err)
	}
	if seatCount != 1 {
		t.Errorf("booking_seats rows = %d, want 1 (duplicate must not retroactively free the seat)", seatCount)
	}
}

func TestHandlePaymentCallback_PaymentNotFound(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	_, err := store.HandlePaymentCallback(ctx, "no-such-payment-id", true)
	if !errors.Is(err, booking.ErrPaymentNotFound) {
		t.Fatalf("err = %v, want ErrPaymentNotFound", err)
	}
}

// TestConcurrentHandlePaymentCallback_DuplicatesAreSafe is the Task
// 5/7-style concurrency test for this task: many goroutines deliver the
// *same* callback simultaneously -- the realistic version of
// payment-svc's DUPLICATE_RATE chaos, where duplicates can arrive close
// enough together to genuinely race, not just one-after-the-other.
func TestConcurrentHandlePaymentCallback_DuplicatesAreSafe(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 1)
	held, err := store.CreateHold(ctx, showID, uuid.New(), seatIDs, uuid.NewString())
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}
	paymentID := uuid.NewString()
	if _, err := store.Checkout(ctx, held.ID, paymentID); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	const callbacks = 50
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(callbacks)
	for i := 0; i < callbacks; i++ {
		go func() {
			defer wg.Done()
			<-start
			if _, err := store.HandlePaymentCallback(ctx, paymentID, true); err != nil {
				t.Errorf("HandlePaymentCallback: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()

	var status string
	err = pool.QueryRow(ctx, `SELECT status FROM bookings WHERE id = $1`, held.ID).Scan(&status)
	if err != nil {
		t.Fatalf("query booking status: %v", err)
	}
	if status != string(booking.StatusConfirmed) {
		t.Errorf("status = %q, want %q", status, booking.StatusConfirmed)
	}

	var seatCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM booking_seats WHERE booking_id = $1`, held.ID).Scan(&seatCount)
	if err != nil {
		t.Fatalf("count booking_seats: %v", err)
	}
	if seatCount != 1 {
		t.Errorf("booking_seats rows = %d, want 1 (50 concurrent duplicate callbacks must apply the effect exactly once)", seatCount)
	}

	testutil.AssertInvariants(t, ctx, pool)
}

func TestCancelBooking_Success(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 1)
	held, err := store.CreateHold(ctx, showID, uuid.New(), seatIDs, uuid.NewString())
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}

	b, err := store.CancelBooking(ctx, held.ID)
	if err != nil {
		t.Fatalf("CancelBooking: %v", err)
	}
	if b.Status != booking.StatusCancelled {
		t.Errorf("status = %q, want %q", b.Status, booking.StatusCancelled)
	}

	var seatCount int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM booking_seats WHERE booking_id = $1`, held.ID).Scan(&seatCount)
	if err != nil {
		t.Fatalf("count booking_seats: %v", err)
	}
	if seatCount != 0 {
		t.Errorf("booking_seats rows = %d, want 0", seatCount)
	}

	testutil.AssertInvariants(t, ctx, pool)
}

func TestCancelBooking_FailsIfNotHeld(t *testing.T) {
	ctx := context.Background()
	pool := testutil.NewPool(t)
	store := booking.NewStore(pool)

	showID, seatIDs := seedShowAndSeats(t, ctx, pool, 1)
	held, err := store.CreateHold(ctx, showID, uuid.New(), seatIDs, uuid.NewString())
	if err != nil {
		t.Fatalf("CreateHold: %v", err)
	}
	if _, err := store.Checkout(ctx, held.ID, uuid.NewString()); err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	_, err = store.CancelBooking(ctx, held.ID)
	if !errors.Is(err, booking.ErrBookingNotHeld) {
		t.Fatalf("CancelBooking error = %v, want ErrBookingNotHeld", err)
	}
}
