-- An idempotency key alone isn't enough: if a caller reuses the same key
-- for a genuinely different request (different seats), naively returning
-- the original booking would silently ignore their new request. The
-- fingerprint is a hash of the request's actual parameters (show + seat
-- set), so a retry can be told apart from a misused key.
--
-- Added as NOT NULL DEFAULT '' then the default is dropped immediately:
-- this lets the column exist safely on a table that may already have
-- rows (they get '', which is fine for dev data we don't care about),
-- while every future INSERT is required to supply a real value.
ALTER TABLE bookings ADD COLUMN request_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE bookings ALTER COLUMN request_fingerprint DROP DEFAULT;
