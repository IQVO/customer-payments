CREATE TABLE payment_authorizations (
    authorization_id         text        PRIMARY KEY,
    checkout_session_id      text        NOT NULL,
    order_id                 text,
    payment_method_token     text        NOT NULL,
    currency                 text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    total_minor              bigint      NOT NULL CHECK (total_minor >= 0),
    state                    text        NOT NULL CHECK (state IN ('AUTHORIZED', 'PARTIALLY_CAPTURED', 'CAPTURED', 'RELEASED', 'VOIDED', 'EXPIRED', 'DECLINED')),
    terminal                 text        CHECK (terminal IN ('VOIDED', 'EXPIRED', 'DECLINED')),
    decline_reason           text,
    provider_authorization_id text,
    expires_at               timestamptz,
    created_at               timestamptz NOT NULL,
    reauthorizations         integer     NOT NULL DEFAULT 0,
    version                  bigint      NOT NULL CHECK (version >= 1)
);
-- one live (not DECLINED) authorization per checkout session
CREATE UNIQUE INDEX payment_authorizations_live_session_idx ON payment_authorizations (checkout_session_id) WHERE state <> 'DECLINED';
CREATE INDEX payment_authorizations_session_idx ON payment_authorizations (checkout_session_id, created_at);
CREATE UNIQUE INDEX payment_authorizations_order_idx ON payment_authorizations (order_id) WHERE order_id IS NOT NULL AND state <> 'DECLINED';
CREATE INDEX payment_authorizations_reauth_idx ON payment_authorizations (expires_at) WHERE state IN ('AUTHORIZED', 'PARTIALLY_CAPTURED');
CREATE INDEX payment_authorizations_unbound_idx ON payment_authorizations (created_at) WHERE state = 'AUTHORIZED' AND order_id IS NULL;

CREATE TABLE allocations (
    authorization_id    text    NOT NULL REFERENCES payment_authorizations (authorization_id) ON DELETE CASCADE,
    group_no            integer NOT NULL CHECK (group_no >= 1),
    amount_minor        bigint  NOT NULL CHECK (amount_minor >= 0),
    state               text    NOT NULL CHECK (state IN ('PENDING', 'CAPTURED', 'RELEASED')),
    captured_minor      bigint  NOT NULL DEFAULT 0 CHECK (captured_minor >= 0),
    tracking_numbers    text[]  NOT NULL DEFAULT '{}',
    provider_capture_id text,
    PRIMARY KEY (authorization_id, group_no)
);
CREATE INDEX allocations_tracking_idx ON allocations USING gin (tracking_numbers);

CREATE TABLE refunds (
    refund_id          text        PRIMARY KEY,
    authorization_id   text        NOT NULL REFERENCES payment_authorizations (authorization_id),
    order_id           text        NOT NULL,
    group_no           integer     NOT NULL CHECK (group_no >= 0),
    currency           text        NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    amount_minor       bigint      NOT NULL CHECK (amount_minor >= 1),
    reason             text        NOT NULL CHECK (reason IN ('LOST', 'RETURNED_TO_SENDER', 'MANUAL')),
    state              text        NOT NULL CHECK (state IN ('REQUESTED', 'SUCCEEDED', 'FAILED')),
    attempts           integer     NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    failure_code       text,
    provider_refund_id text,
    created_at         timestamptz NOT NULL,
    version            bigint      NOT NULL CHECK (version >= 1)
);
CREATE INDEX refunds_order_idx ON refunds (order_id, created_at);
CREATE INDEX refunds_authorization_idx ON refunds (authorization_id, created_at);
CREATE INDEX refunds_requested_idx ON refunds (created_at) WHERE state = 'REQUESTED';

CREATE TABLE parked_facts (
    event_id         text        PRIMARY KEY,
    kind             text        NOT NULL CHECK (kind IN ('SHIPPED', 'CANCELLED', 'UNSOURCEABLE', 'LOST', 'RETURNED')),
    order_id         text        NOT NULL,
    group_no         integer     NOT NULL DEFAULT 0,
    tracking_numbers text[]      NOT NULL DEFAULT '{}',
    occurred_at      timestamptz NOT NULL,
    parked_at        timestamptz NOT NULL,
    escalated_at     timestamptz
);
CREATE INDEX parked_facts_order_idx ON parked_facts (order_id, parked_at);
CREATE INDEX parked_facts_parked_idx ON parked_facts (parked_at);

CREATE TABLE processed_events (
    event_id     text        PRIMARY KEY,
    event_type   text        NOT NULL,
    processed_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE idempotency_keys (
    route        text        NOT NULL,
    key          text        NOT NULL,
    request_hash text        NOT NULL,
    status       integer     NOT NULL,
    content_type text        NOT NULL,
    body         bytea       NOT NULL,
    created_at   timestamptz NOT NULL,
    PRIMARY KEY (route, key)
);
CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);

CREATE TABLE outbox_events (
    id           bigserial   PRIMARY KEY,
    event_id     text        NOT NULL UNIQUE,
    topic        text        NOT NULL,
    event_type   text        NOT NULL,
    subject      text        NOT NULL,
    key          bytea       NOT NULL,
    dataschema   text        NOT NULL,
    value        bytea       NOT NULL,
    headers      jsonb       NOT NULL DEFAULT '[]'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz,
    attempts     integer     NOT NULL DEFAULT 0,
    last_error   text
);
CREATE INDEX outbox_events_unpublished_idx ON outbox_events (id) WHERE published_at IS NULL;
