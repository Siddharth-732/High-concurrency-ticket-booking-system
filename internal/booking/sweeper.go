package booking

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
)

func (s *Store) SweepExpiredHolds(ctx context.Context) (int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM bookings WHERE status = $1 AND expires_at <= now()
	`, StatusHeld)
	if err != nil {
		return 0, fmt.Errorf("query expired holds: %w", err)
	}

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired hold id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate expired holds: %w", err)
	}

	var released int
	var errs []error
	for _, id := range ids {
		ok, err := s.expireOne(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("booking %s: %w", id, err))
			continue
		}
		if ok {
			released++
		}
	}

	if len(errs) > 0 {
		return released, errors.Join(errs...)
	}
	return released, nil
}

func (s *Store) expireOne(ctx context.Context, bookingID uuid.UUID) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var status Status
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT status, expires_at FROM bookings WHERE id = $1 FOR UPDATE
	`, bookingID).Scan(&status, &expiresAt)
	if err != nil {
		return false, fmt.Errorf("lock booking: %w", err)
	}

	// Re-check under the lock: something else (a confirm, a cancel, or
	// another sweep) may have already moved this booking on since we
	// listed it as a candidate.
	if status != StatusHeld || expiresAt.After(time.Now()) {
		return false, nil
	}

	if _, err := tx.Exec(ctx, `DELETE FROM booking_seats WHERE booking_id = $1`, bookingID); err != nil {
		return false, fmt.Errorf("release seats: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE bookings SET status = $1, updated_at = now() WHERE id = $2`, StatusExpired, bookingID); err != nil {
		return false, fmt.Errorf("mark expired: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return true, nil
}

func (s *Store) RunSweeper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			released, err := s.SweepExpiredHolds(ctx)
			if err != nil {
				log.Printf("sweeper: error releasing expired holds: %v", err)
			}
			if released > 0 {
				log.Printf("sweeper: released %d expired hold(s)", released)
			}
		}
	}
}
