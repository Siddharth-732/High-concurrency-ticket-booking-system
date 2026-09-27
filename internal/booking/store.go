// This is to define that this package belong to booking
package booking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgreSQL SQLSTATE for unique violations
const uniqueViolation = "23505"

// Store struct defenition
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructor
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// CreateHold method create a booking hold,
/*
	s - reciever , then comes the function name, inside () are the parameters
	ctx - it holds the context of the request
	showID, userID- are the unique ids
	seatIDs - array of unique ids of seats for which the user wants to book
	idempotencyKey - unique key to prevent duplicate bookings
*/
// *Booking is the struct that is returned by the function along with the error
func (s *Store) CreateHold(ctx context.Context, showID, userID uuid.UUID, seatIDs []uuid.UUID, idempotencyKey string) (*Booking, error) {
	if len(seatIDs) == 0 {
		return nil, ErrNoSeats
	}

	// tx start the transaction
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}

	// tx.Rollback(ctx) - rollbacks the transaction if it is not committed, if commit is done then it does nothing
	defer tx.Rollback(ctx)

	expiresAt := time.Now().Add(HoldTTL)

	// create a variable with booking Datatype
	var b Booking
	err = tx.QueryRow(ctx, `
		INSERT INTO bookings (show_id, user_id, status, idempotency_key, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, show_id, user_id, status, idempotency_key, expires_at, created_at, updated_at
	`, showID, userID, StatusHeld, idempotencyKey, expiresAt).Scan(
		&b.ID, &b.ShowID, &b.UserID, &b.Status,
		&b.IdempotencyKey, &b.ExpiresAt, &b.CreatedAt, &b.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateIdempotencyKey
		}
		return nil, fmt.Errorf("insert booking: %w", err)
	}

	// loop over the seatIDs array and adds them to the booking table
	for _, seatID := range seatIDs {
		_, err := tx.Exec(ctx, `
			INSERT INTO booking_seats (booking_id, seat_id) VALUES ($1, $2)
		`, b.ID, seatID)
		if err != nil { // if an error is found the booking link can't be performed
			if isUniqueViolation(err) {
				return nil, ErrSeatUnavailable
			}
			return nil, fmt.Errorf("insert booking_seats: %w", err)
		}
	}
	// tx.Commit(ctx) - commits the transaction, if not committed the data is not stored in the database
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// return b if everthing is fine
	b.SeatIDs = seatIDs
	return &b, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
