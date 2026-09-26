# Design Notes

This file records the trade-offs behind decisions in this project, in the
order they were made. Every claim in the README should trace back to a
decision here and a test that proves it.

## Migrations over hand-run SQL (Task 2)

Schema changes are numbered, ordered SQL files applied by `golang-migrate`,
tracked in a `schema_migrations` table, and reversible via `.down.sql`
files. This is how schema changes ship in production: reviewed, versioned,
applied identically across every environment, instead of "someone ran some
SQL once."

## Redis fails open (stated up front, implemented later)

The rate limiter uses Redis. If Redis is unreachable, requests are allowed
through and the failure is logged, rather than rejected. Booking
correctness (no seat sold twice) must never depend on Redis being up --
that guarantee lives entirely in Postgres. Rate limiting is a
nice-to-have; losing it temporarily is a much smaller problem than an
outage in a dependency taking down checkout entirely.

## The core invariant: no seat sold twice (Task 3)

Two designs were considered for representing seat availability:

**Option A -- mutable state.** A `status` column on `seats`
(`available`/`held`/`booked`), flipped via `SELECT ... FOR UPDATE` in every
code path that touches a seat. Rejected: correctness would depend on every
call site (hold, expiry sweeper, payment callback, cancel) locking and
updating the column correctly. A bug in any one of them lets the column
drift from reality.

**Option B -- claim as a row (chosen).** `booking_seats(booking_id,
seat_id)` has a plain `UNIQUE` constraint on `seat_id`. A seat is claimed
exactly when a row for it exists -- no status column, no derived state to
keep in sync. Two transactions racing to claim the same seat both attempt
an `INSERT`; Postgres's unique index guarantees only one succeeds, and the
losing transaction's whole `CreateHold` is rolled back, so a multi-seat
hold is all-or-nothing for free, without explicit row locking.

Releasing a hold (expiry, cancel, failed payment) deletes the row.
Confirming a booking leaves it in place permanently.

**Why this over Option A:** the invariant is enforced by a database
constraint that no application bug can bypass, rather than by locking
discipline that has to be gotten right in every code path. The concurrency
test in Task 5 is asserting "the constraint holds under load," not
"our locking code behaved correctly under load."

**Trade-off accepted:** `GetSeatMap` needs a `LEFT JOIN` against
`booking_seats` to know which seats are free, instead of reading a single
indexed column. At this project's scale that cost is irrelevant.

No `users` table exists. There is no auth service in scope; `user_id` is
an opaque UUID supplied by the caller.
