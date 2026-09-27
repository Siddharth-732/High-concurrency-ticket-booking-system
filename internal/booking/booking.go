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
	ErrNoSeats                 = errors.New("booking: at least one seat is required")
	ErrSeatUnavailable         = errors.New("booking: one or more seats are already held or booked")
	ErrDuplicateIdempotencyKey = errors.New("booking: idempotency key already used")
)
