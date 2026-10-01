// this is to define that this package is for booking only
package booking

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// a seat-hold lifetime of 5 minutes
const HoldTTL = 5 * time.Minute

// Booking status type defenitions
type Status string

// allowed booking-status constants
const (
	StatusHeld            Status = "held"
	StatusAwaitingPayment Status = "awaiting_payment"
	StatusConfirmed       Status = "confirmed"
	StatusCancelled       Status = "cancelled"
	StatusExpired         Status = "expired"
	StatusFailed          Status = "failed"
)

// Booking stuct defenitions
type Booking struct {
	ID             uuid.UUID
	ShowID         uuid.UUID
	UserID         uuid.UUID
	Status         Status
	IdempotencyKey string
	SeatIDs        []uuid.UUID
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Errors returned by booking package
var (
	ErrNoSeats         = errors.New("booking: at least one seat is required")
	ErrSeatUnavailable = errors.New("booking: one or more seats are already held or booked")

	// ErrIdempotencyKeyReused means this user's idempotency key was already used for a request with *different* parameters.
	// A true retry (same key, same parameters) is not an error: CreateHold returns the original booking instead.
	// This error only fires on genuine misuse, e.g. a client-side bug that reuses a key across two unrelated requests.
	ErrIdempotencyKeyReused = errors.New("booking: idempotency key already used for a different request")

	// ErrBookingNotFound means no booking exists with the given id.
	ErrBookingNotFound = errors.New("booking: not found")

	// ErrBookingNotHeld means the caller tried to do something that only
	// makes sense for a booking still in the "held" state (Checkout,
	// CancelBooking), but the booking has already moved on -- to
	// awaiting_payment, confirmed, cancelled, expired, or failed.
	ErrBookingNotHeld = errors.New("booking: not in held state")

	// ErrHoldExpired means Checkout was called after the hold's 5 minutes
	// ran out, but the sweeper has not released it yet. The caller must
	// create a new hold; this one cannot be checked out.
	ErrHoldExpired = errors.New("booking: hold has expired")

	// ErrPaymentNotFound means HandlePaymentCallback was called with an
	// external_payment_id that Checkout never created. This should not
	// happen in practice, since only Checkout creates payments rows.
	ErrPaymentNotFound = errors.New("booking: payment not found")
)
