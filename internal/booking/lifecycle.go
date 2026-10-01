package booking

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Checkout moves a held booking into awaiting_payment, standing in for
// the moment a real payment-svc (Task 12) would be asked to create a
// payment. externalPaymentID is supplied by the caller because nothing
// in this package can generate one yet -- it plays the role of "the id
// payment-svc already handed back to us."
//
// Like the sweeper, this locks the booking row with FOR UPDATE and
// re-checks its state under that lock: the booking could have expired,
// or (in principle) already be past "held", between whatever caller
// decided to check out and this function actually running.
func (s *Store) Checkout(ctx context.Context, bookingID uuid.UUID, externalPaymentID string) (*Booking, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var b Booking
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT id, show_id, user_id, status, idempotency_key, expires_at, created_at, updated_at
		FROM bookings WHERE id = $1 FOR UPDATE
	`, bookingID).Scan(&b.ID, &b.ShowID, &b.UserID, &b.Status, &b.IdempotencyKey, &expiresAt, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, ErrBookingNotFound
	}
	b.ExpiresAt = expiresAt

	if b.Status != StatusHeld {
		return nil, ErrBookingNotHeld
	}
	if expiresAt.Before(time.Now()) {
		// Not yet released by the sweeper, but its time is up -- refuse
		// rather than check out a hold that is effectively already gone.
		return nil, ErrHoldExpired
	}

	var amountCents int64
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(sum(s.price_cents), 0)
		FROM booking_seats bs JOIN seats s ON s.id = bs.seat_id
		WHERE bs.booking_id = $1
	`, bookingID).Scan(&amountCents)
	if err != nil {
		return nil, fmt.Errorf("sum seat prices: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO payments (booking_id, external_payment_id, amount_cents, status)
		VALUES ($1, $2, $3, 'pending')
	`, bookingID, externalPaymentID, amountCents); err != nil {
		return nil, fmt.Errorf("insert payment: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status = $1, updated_at = now() WHERE id = $2
	`, StatusAwaitingPayment, bookingID); err != nil {
		return nil, fmt.Errorf("mark awaiting_payment: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	b.Status = StatusAwaitingPayment
	if seatIDs, err := s.seatIDsForBooking(ctx, bookingID); err == nil {
		b.SeatIDs = seatIDs
	}
	return &b, nil
}

// HandlePaymentCallback applies the outcome of a payment -- called by
// payment-svc, potentially more than once for the same payment (the
// project's fake gateway deliberately duplicates callbacks at
// DUPLICATE_RATE to exercise exactly this).
//
// The payments row is locked first, before the booking row. If that
// payment has already been processed (status is no longer "pending"),
// this call is a duplicate: it is a no-op, not an error, and whatever
// outcome this duplicate claims is ignored -- the first callback to
// arrive is the one that counts. A later duplicate cannot flip a
// confirmed booking back to failed, or vice versa.
func (s *Store) HandlePaymentCallback(ctx context.Context, externalPaymentID string, success bool) (*Booking, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var paymentID, bookingID uuid.UUID
	var paymentStatus string
	err = tx.QueryRow(ctx, `
		SELECT id, booking_id, status FROM payments WHERE external_payment_id = $1 FOR UPDATE
	`, externalPaymentID).Scan(&paymentID, &bookingID, &paymentStatus)
	if err != nil {
		return nil, ErrPaymentNotFound
	}

	if paymentStatus != "pending" {
		// Already processed by an earlier callback for this same
		// payment -- return the current state as-is, applying nothing.
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}
		return s.bookingByID(ctx, bookingID)
	}

	var b Booking
	err = tx.QueryRow(ctx, `
		SELECT id, show_id, user_id, status, idempotency_key, expires_at, created_at, updated_at
		FROM bookings WHERE id = $1 FOR UPDATE
	`, bookingID).Scan(&b.ID, &b.ShowID, &b.UserID, &b.Status, &b.IdempotencyKey, &b.ExpiresAt, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("lock booking: %w", err)
	}

	newPaymentStatus := "failed"
	newBookingStatus := StatusFailed
	if success {
		newPaymentStatus = "succeeded"
		newBookingStatus = StatusConfirmed
	}

	if _, err := tx.Exec(ctx, `
		UPDATE payments SET status = $1, updated_at = now() WHERE id = $2
	`, newPaymentStatus, paymentID); err != nil {
		return nil, fmt.Errorf("update payment: %w", err)
	}

	if !success {
		// Payment failed: the seats must be freed for someone else,
		// exactly like an expired or cancelled hold.
		if _, err := tx.Exec(ctx, `DELETE FROM booking_seats WHERE booking_id = $1`, bookingID); err != nil {
			return nil, fmt.Errorf("release seats: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status = $1, updated_at = now() WHERE id = $2
	`, newBookingStatus, bookingID); err != nil {
		return nil, fmt.Errorf("update booking status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	b.Status = newBookingStatus
	if success {
		if seatIDs, err := s.seatIDsForBooking(ctx, bookingID); err == nil {
			b.SeatIDs = seatIDs
		}
	}
	return &b, nil
}

// CancelBooking releases a hold the user no longer wants, before they
// have checked out. Scoped deliberately to the "held" state only:
// cancelling mid-payment (awaiting_payment) would mean coordinating with
// whatever payment-svc is doing at that exact moment, which is a bigger
// feature than this project currently builds.
func (s *Store) CancelBooking(ctx context.Context, bookingID uuid.UUID) (*Booking, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var b Booking
	err = tx.QueryRow(ctx, `
		SELECT id, show_id, user_id, status, idempotency_key, expires_at, created_at, updated_at
		FROM bookings WHERE id = $1 FOR UPDATE
	`, bookingID).Scan(&b.ID, &b.ShowID, &b.UserID, &b.Status, &b.IdempotencyKey, &b.ExpiresAt, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, ErrBookingNotFound
	}

	if b.Status != StatusHeld {
		return nil, ErrBookingNotHeld
	}

	if _, err := tx.Exec(ctx, `DELETE FROM booking_seats WHERE booking_id = $1`, bookingID); err != nil {
		return nil, fmt.Errorf("release seats: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bookings SET status = $1, updated_at = now() WHERE id = $2
	`, StatusCancelled, bookingID); err != nil {
		return nil, fmt.Errorf("mark cancelled: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	b.Status = StatusCancelled
	return &b, nil
}

// bookingByID is a plain, unlocked read used to build a return value
// after a transaction that modified (or chose not to modify) a booking
// has already committed.
func (s *Store) bookingByID(ctx context.Context, bookingID uuid.UUID) (*Booking, error) {
	var b Booking
	err := s.pool.QueryRow(ctx, `
		SELECT id, show_id, user_id, status, idempotency_key, expires_at, created_at, updated_at
		FROM bookings WHERE id = $1
	`, bookingID).Scan(&b.ID, &b.ShowID, &b.UserID, &b.Status, &b.IdempotencyKey, &b.ExpiresAt, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, ErrBookingNotFound
	}
	if seatIDs, err := s.seatIDsForBooking(ctx, bookingID); err == nil {
		b.SeatIDs = seatIDs
	}
	return &b, nil
}
