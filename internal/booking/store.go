// This is to define that this package belong to booking
package booking

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

	fingerprint := fingerprintRequest(showID, seatIDs)

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
		INSERT INTO bookings (show_id, user_id, status, idempotency_key, request_fingerprint, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, show_id, user_id, status, idempotency_key, expires_at, created_at, updated_at
	`, showID, userID, StatusHeld, idempotencyKey, fingerprint, expiresAt).Scan(
		&b.ID, &b.ShowID, &b.UserID, &b.Status,
		&b.IdempotencyKey, &b.ExpiresAt, &b.CreatedAt, &b.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			// Someone (possibly this exact caller, retrying) already used
			// this (user_id, idempotency_key) pair. Roll back explicitly,
			// right now, before doing anything else: tx is aborted but
			// still holds a pool connection until Rollback runs, and
			// resolveDuplicateIdempotencyKey needs a *second* connection
			// from the same pool. Leaving that first connection held open
			// until the deferred Rollback fires (i.e. until this whole
			// function returns) starved the pool under real concurrency --
			// every losing goroutine held one connection and blocked
			// forever waiting for another. Releasing it first avoids that.
			_ = tx.Rollback(ctx)
			return s.resolveDuplicateIdempotencyKey(ctx, userID, idempotencyKey, fingerprint)
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

// resolveDuplicateIdempotencyKey runs after CreateHold's own INSERT hits
// the (user_id, idempotency_key) UNIQUE constraint. It looks up whichever
// booking won that race and decides what the *caller* should see:
//
//   - same fingerprint (same show + same seats) -> this is a genuine
//     retry. Return the original booking as if this call had created it;
//     the caller cannot tell the difference between "I made this booking
//     just now" and "I already made this booking a moment ago."
//   - different fingerprint -> the key was reused for a different
//     request. That is a client bug, not a retry, so we refuse rather
//     than silently returning a booking for seats the caller didn't ask
//     for this time.
//
// It queries s.pool directly, not the aborted transaction from
// CreateHold: once an INSERT inside a transaction errors, Postgres will
// not run any further statements on that transaction until it is rolled
// back, so a fresh connection from the pool is required here.
func (s *Store) resolveDuplicateIdempotencyKey(ctx context.Context, userID uuid.UUID, idempotencyKey, fingerprint string) (*Booking, error) {
	var b Booking
	var storedFingerprint string
	err := s.pool.QueryRow(ctx, `
		SELECT id, show_id, user_id, status, idempotency_key, request_fingerprint, expires_at, created_at, updated_at
		FROM bookings
		WHERE user_id = $1 AND idempotency_key = $2
	`, userID, idempotencyKey).Scan(
		&b.ID, &b.ShowID, &b.UserID, &b.Status, &b.IdempotencyKey,
		&storedFingerprint, &b.ExpiresAt, &b.CreatedAt, &b.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("look up existing booking for idempotency key: %w", err)
	}

	if storedFingerprint != fingerprint {
		return nil, ErrIdempotencyKeyReused
	}

	seatIDs, err := s.seatIDsForBooking(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	b.SeatIDs = seatIDs
	return &b, nil
}

func (s *Store) seatIDsForBooking(ctx context.Context, bookingID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT seat_id FROM booking_seats WHERE booking_id = $1`, bookingID)
	if err != nil {
		return nil, fmt.Errorf("query booking_seats: %w", err)
	}
	defer rows.Close()

	seatIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("collect seat ids: %w", err)
	}
	return seatIDs, nil
}

// fingerprintRequest hashes the parts of a hold request that must match
// for two calls to count as "the same request": the show and the exact
// set of seats. Seat IDs are sorted first so that requesting {A, B} and
// {B, A} produce the same fingerprint -- the caller's ordering shouldn't
// matter, only the set.
func fingerprintRequest(showID uuid.UUID, seatIDs []uuid.UUID) string {
	sorted := make([]string, len(seatIDs))
	for i, id := range seatIDs {
		sorted[i] = id.String()
	}
	sort.Strings(sorted)

	h := sha256.New()
	h.Write([]byte(showID.String()))
	h.Write([]byte("|"))
	h.Write([]byte(strings.Join(sorted, ",")))
	return hex.EncodeToString(h.Sum(nil))
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}
