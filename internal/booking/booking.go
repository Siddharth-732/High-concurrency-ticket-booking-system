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
)
