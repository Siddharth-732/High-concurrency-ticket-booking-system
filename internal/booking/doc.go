// Package booking holds the core booking logic: seat holds, checkout,
// payment callbacks and expiry. It talks to PostgreSQL directly and knows
// nothing about gRPC, so the invariants can be tested without a network.
package booking
