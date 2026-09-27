package booking

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

const HoldTTL = 5 * time.Minute

type Status string

const (
	StatusHeld            Status = "held"
	StatusAwaitingPayment Status = "awaiting_payment"
	StatusConfirmed       Status = "confirmed"
	StatusCancelled       Status = "cancelled"
	StatusExpired         Status = "expired"
	StatusFailed          Status = "failed"
)

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

var (
	ErrNoSeats                 = errors.New("booking: at least one seat is required")
	ErrSeatUnavailable         = errors.New("booking: one or more seats are already held or booked")
	ErrDuplicateIdempotencyKey = errors.New("booking: idempotency key already used")
)
