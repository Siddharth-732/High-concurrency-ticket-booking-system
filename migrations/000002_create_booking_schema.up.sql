CREATE TABLE shows (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    venue      TEXT NOT NULL,
    starts_at  TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE seats (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    show_id     UUID NOT NULL REFERENCES shows(id) ON DELETE CASCADE,
    label       TEXT NOT NULL,
    price_cents BIGINT NOT NULL CHECK (price_cents >= 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (show_id, label)
);

CREATE TABLE bookings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    show_id         UUID NOT NULL REFERENCES shows(id),
    user_id         UUID NOT NULL,
    status          TEXT NOT NULL DEFAULT 'held' CHECK (status IN ('held', 'awaiting_payment', 'confirmed', 'cancelled', 'expired', 'failed')),
    idempotency_key TEXT NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key)
);

CREATE INDEX bookings_status_expires_at_idx ON bookings (status, expires_at);

CREATE TABLE booking_seats (
    booking_id UUID NOT NULL REFERENCES bookings(id) ON DELETE CASCADE,
    seat_id    UUID NOT NULL REFERENCES seats(id),
    PRIMARY KEY (booking_id, seat_id),
    UNIQUE (seat_id)
);

CREATE TABLE payments (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    booking_id          UUID NOT NULL REFERENCES bookings(id),
    external_payment_id TEXT NOT NULL,
    amount_cents        BIGINT NOT NULL CHECK (amount_cents >= 0), status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'succeeded', 'failed')),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(), UNIQUE (external_payment_id)
);

CREATE INDEX payments_booking_id_idx ON payments (booking_id);
