-- gen_random_uuid() gives us UUID primary keys without a round trip to the
-- application to generate them. Seats, holds and bookings will all use it.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
