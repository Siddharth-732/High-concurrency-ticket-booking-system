// Package invariant audits the database for violations of rules the rest
// of the system assumes always hold. It has no dependency on "testing" --
// this is a genuine data-integrity check that could run as a one-off
// admin audit against a live database, not just glue for tests. Tests
// reach it through internal/testutil.AssertInvariants.
package invariant

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Violation describes one broken rule found in the database.
type Violation struct {
	Rule   string
	Detail string
}

func (v Violation) String() string {
	return fmt.Sprintf("[%s] %s", v.Rule, v.Detail)
}

// checkFn is one independent rule. It returns every row that breaks the
// rule, so a single run of Check reports all violations at once instead
// of stopping at the first.
type checkFn func(ctx context.Context, pool *pgxpool.Pool) ([]Violation, error)

var checks = []checkFn{
	checkNoSeatClaimedTwice,
	checkNoClaimOnReleasedBooking,
	checkSeatBelongsToBookingsShow,
}

// Check runs every known rule against pool and returns every violation
// found. A nil/empty slice with a nil error means the database is
// consistent.
func Check(ctx context.Context, pool *pgxpool.Pool) ([]Violation, error) {
	var all []Violation
	for _, check := range checks {
		vs, err := check(ctx, pool)
		if err != nil {
			return nil, err
		}
		all = append(all, vs...)
	}
	return all, nil
}

// checkNoSeatClaimedTwice is deliberately redundant with the seat_id
// UNIQUE constraint from Task 3: that constraint should make this
// impossible at the database level already. It stays here as a
// defense-in-depth check, so a future migration that weakens or drops
// that constraint by accident gets caught by name ("seat_claimed_twice")
// instead of the system just quietly losing the guarantee.
func checkNoSeatClaimedTwice(ctx context.Context, pool *pgxpool.Pool) ([]Violation, error) {
	rows, err := pool.Query(ctx, `
		SELECT seat_id, count(*) AS claims
		FROM booking_seats
		GROUP BY seat_id
		HAVING count(*) > 1
	`)
	if err != nil {
		return nil, fmt.Errorf("checkNoSeatClaimedTwice: %w", err)
	}
	defer rows.Close()

	var violations []Violation
	for rows.Next() {
		var seatID string
		var claims int
		if err := rows.Scan(&seatID, &claims); err != nil {
			return nil, fmt.Errorf("checkNoSeatClaimedTwice: scan: %w", err)
		}
		violations = append(violations, Violation{
			Rule:   "seat_claimed_twice",
			Detail: fmt.Sprintf("seat %s has %d booking_seats rows, want at most 1", seatID, claims),
		})
	}
	return violations, rows.Err()
}

// checkNoClaimOnReleasedBooking: releasing a hold (expiry, cancel, failed
// payment) is supposed to delete the booking's booking_seats rows in the
// same transaction that marks it released. If a row still exists for a
// booking already in a released state, some release code path forgot to
// delete it -- the seat looks claimed to GetSeatMap while the booking
// that claimed it is dead.
func checkNoClaimOnReleasedBooking(ctx context.Context, pool *pgxpool.Pool) ([]Violation, error) {
	rows, err := pool.Query(ctx, `
		SELECT bs.seat_id, bs.booking_id, b.status
		FROM booking_seats bs
		JOIN bookings b ON b.id = bs.booking_id
		WHERE b.status IN ('cancelled', 'expired', 'failed')
	`)
	if err != nil {
		return nil, fmt.Errorf("checkNoClaimOnReleasedBooking: %w", err)
	}
	defer rows.Close()

	var violations []Violation
	for rows.Next() {
		var seatID, bookingID, status string
		if err := rows.Scan(&seatID, &bookingID, &status); err != nil {
			return nil, fmt.Errorf("checkNoClaimOnReleasedBooking: scan: %w", err)
		}
		violations = append(violations, Violation{
			Rule:   "claim_on_released_booking",
			Detail: fmt.Sprintf("seat %s still claimed by booking %s, which is %q", seatID, bookingID, status),
		})
	}
	return violations, rows.Err()
}

// checkSeatBelongsToBookingsShow: a booking_seats row should only ever
// link a booking to a seat from that same booking's show. A mismatch
// here means seats from different shows got cross-wired somewhere.
func checkSeatBelongsToBookingsShow(ctx context.Context, pool *pgxpool.Pool) ([]Violation, error) {
	rows, err := pool.Query(ctx, `
		SELECT bs.seat_id, bs.booking_id, s.show_id, b.show_id
		FROM booking_seats bs
		JOIN bookings b ON b.id = bs.booking_id
		JOIN seats s ON s.id = bs.seat_id
		WHERE s.show_id <> b.show_id
	`)
	if err != nil {
		return nil, fmt.Errorf("checkSeatBelongsToBookingsShow: %w", err)
	}
	defer rows.Close()

	var violations []Violation
	for rows.Next() {
		var seatID, bookingID, seatShowID, bookingShowID string
		if err := rows.Scan(&seatID, &bookingID, &seatShowID, &bookingShowID); err != nil {
			return nil, fmt.Errorf("checkSeatBelongsToBookingsShow: scan: %w", err)
		}
		violations = append(violations, Violation{
			Rule: "seat_show_mismatch",
			Detail: fmt.Sprintf("booking %s (show %s) claims seat %s (show %s)",
				bookingID, bookingShowID, seatID, seatShowID),
		})
	}
	return violations, rows.Err()
}
