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

const uniqueViolation = "23505"

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

func (s *Store) CreateHold(ctx context.Context, showID, userID uuid.UUID, seatIDs []uuid.UUID, idempotencyKey string) (*Booking, error) {
	if len(seatIDs) == 0 {
		return nil, ErrNoSeats
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}

	defer tx.Rollback(ctx)

	expiresAt := time.Now().Add(HoldTTL)

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

	for _, seatID := range seatIDs {
		_, err := tx.Exec(ctx, `
			INSERT INTO booking_seats (booking_id, seat_id) VALUES ($1, $2)
		`, b.ID, seatID)
		if err != nil {
			if isUniqueViolation(err) {
				return nil, ErrSeatUnavailable
			}
			return nil, fmt.Errorf("insert booking_seats: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	b.SeatIDs = seatIDs
	return &b, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
