# High-Concurrency Ticket Booking System

A ticket-booking backend in Go where thousands of users compete for the same
seats at once, and no seat is ever sold twice, even when payments arrive late
or duplicated and services are killed mid-booking.

**Stack:** Go, gRPC + Protobuf, PostgreSQL, Redis, Docker Compose.

> Work in progress. Every claim in this README will link to the test or
> benchmark that proves it.

## Layout

| Path | Purpose |
|------|---------|
| `cmd/booking-svc` | Entry point for the booking service (the core) |
| `cmd/payment-svc` | Entry point for the fake payment gateway |
| `internal/booking` | Booking logic: holds, checkout, callbacks, expiry |
| `internal/payment` | Fake gateway behaviour (failures, delays, duplicates) |
| `migrations/` | SQL schema migrations |
| `proto/` | Protobuf API definitions |
