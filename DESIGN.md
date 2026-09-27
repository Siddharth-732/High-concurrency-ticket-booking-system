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

## CreateHold, and the local -race toolchain detour (Task 4)

`internal/booking.Store.CreateHold` does not lock anything itself. It
inserts the `bookings` row, then one `booking_seats` row per seat, all in
a single transaction. If any seat insert violates the `UNIQUE(seat_id)`
constraint, the function returns before calling `Commit`, and the deferred
`Rollback` undoes everything -- including the `bookings` row. All-or-
nothing for a multi-seat hold comes from transactional atomicity, not from
`SELECT ... FOR UPDATE` or any lock ordering we have to get right.

Tests run against a real, disposable Postgres container per test run (see
`internal/testutil`, via `testcontainers-go`) rather than the docker-compose
Postgres from Task 2 -- tests must be hermetic and runnable with a bare
`go test`, with no requirement to remember `docker compose up` first.

**Local toolchain note (Windows only):** `go test -race` requires cgo,
which requires a 64-bit C compiler. This machine's `gcc` on PATH is
`MinGW.org GCC` (32-bit only), installed for unrelated C++ work, and left
untouched deliberately. Instead, a separate 64-bit `mingw-w64` toolchain
(`WinLibs`) was installed and pointed to only via the `CC` **user
environment variable** -- PATH order is never touched, so the existing
`gcc` command and C++ setup are unaffected. This is a Windows-development-
machine-only concern: CI (see below) needs none of it.

## Proving the invariant under real concurrency (Task 5)

`internal/booking/concurrency_test.go` is the test the project's headline
claim rests on. Two scenarios, both run with `go test -race`:

1. **`TestConcurrentHold_ExactlyOneWinner`** -- 300 goroutines, released
   at the same instant via a closed channel, all call `CreateHold` for the
   *same single seat* with distinct users. Exactly one must succeed; the
   rest must get `ErrSeatUnavailable`.
2. **`TestConcurrentHold_OverlappingMultiSeatAllOrNothing`** -- two groups
   of goroutines request overlapping seat pairs (`{A,B}` vs `{B,C}`) at
   the same instant. Because every request from one group shares seat `B`
   with every request from the other, at most one request in the entire
   run can succeed -- this proves the all-or-nothing guarantee holds
   under contention, not just in a single-threaded test.

Both tests verify the outcome twice: once from the in-memory tally of
what `CreateHold` returned to each goroutine, and once by querying
Postgres directly afterwards (`SELECT count(*) FROM booking_seats ...`).
The second check is the one that matters -- it confirms the database
itself, not just our bookkeeping of return values, ends up in the
correct state.

Each test run spins up a fresh, disposable Postgres container via
`testcontainers-go` (see `internal/testutil`), so results can never be
polluted by data left over from a previous run, and the same command
(`go test ./...`) works identically on a laptop or in CI with no setup
step required first.

## The invariant checker (Task 6)

`internal/invariant` is a general-purpose data-integrity auditor, not a
test helper: it takes no dependency on the `testing` package, so it could
just as well run as a one-off admin check against a live database. Every
test from here on calls it (via `testutil.AssertInvariants`) as a generic
backstop, on top of whatever specific assertions that test already makes.

Three rules, in order of how likely they are to actually fire:

1. **`seat_claimed_twice`** -- deliberately redundant with the
   `UNIQUE(seat_id)` constraint on `booking_seats`. This rule can never
   fire through any code path in this project, which is itself the point:
   it exists so that if a future migration ever weakens that constraint,
   the failure is a clear, named invariant violation instead of a silent
   loss of the core guarantee.
2. **`claim_on_released_booking`** -- a `booking_seats` row must not
   outlive its parent booking once that booking is `cancelled`,
   `expired`, or `failed`; releasing a hold is supposed to delete the row
   in the same transaction. This is the rule most likely to catch a real
   bug once Task 8 (the expiry sweeper) and `CancelBooking` exist.
3. **`seat_show_mismatch`** -- a booking must only claim seats belonging
   to its own show.

Rules 2 and 3 are tested by manufacturing the exact bug they guard
against with raw SQL that bypasses `Store` entirely (no correct code path
in this codebase can produce either state), then confirming `Check`
reports it. This is the difference between "the checker always says
clean" and "the checker actually detects a broken invariant" -- a checker
that can't be proven to ever fail is not trustworthy.

## Git workflow

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):
`<type>(<scope>): <summary> (Task N)`, e.g.
`feat(booking): CreateHold transaction logic (Task 4)`. Types used in this
repo: `feat`, `fix`, `test`, `docs`, `chore`, `ci`.

Two ways work get committed, chosen per task:

- **Small, self-contained tasks** commit straight to `main`.
- **Larger tasks, or a run of closely related tasks**, get a branch
  (`task-NN-short-name`), a self-opened PR describing the decision and
  trade-off, and a self-merge. This is deliberately a solo-friendly
  version of a real team workflow: a browsable PR history with actual
  design rationale attached, not just commit messages.

`main` is branch-protected with **"require status checks to pass"** --
not "require approving review". As the sole contributor, a required-
review rule would lock the repo, since GitHub does not allow a PR author
to approve their own PR.

Milestone tags (`v0.1.0` after Phase 1, etc.) mark points where a whole
phase of the plan is done and tested.

## CI, moved earlier than originally planned

The original task list put CI (GitHub Actions) in Phase 3, alongside
Docker packaging and load testing, on the theory that it was "polish" for
a finished project. That was wrong: CI's entire value is catching a
regression the moment it's introduced, which matters most during active
development, not after the fact. Waiting until the project was nearly
done would have meant building the riskiest part -- the concurrency and
transaction logic in Tasks 4-9 -- with no automated check at all.

`.github/workflows/ci.yml` was added right after Task 6 instead, running
`go build`, `go vet`, and `go test -race` on every push and PR. It starts
minimal and grows a step at a time as later tasks add more to check:
lint (Task 11), the Docker image build (Task 15), the load test (Task 17)
each add a step to this same file rather than being written from scratch
at the end.

One concrete benefit of moving it early: `ubuntu-latest` runners ship
Docker already running and a 64-bit `gcc` already installed, so CI needs
none of the Windows-specific `CC` workaround described under Task 4 above
-- `go test ./... -race` just works there without extra setup.
