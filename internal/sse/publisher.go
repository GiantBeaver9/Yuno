package sse

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Publisher is the one-method seam (ADR-23). NOTIFY is the default impl;
// Redis/NATS is a later drop-in, no rewrite.
type Publisher interface {
	Publish(ctx context.Context, messageID int64) error
}

// NotifyPublisher emits `SELECT pg_notify(Channel, <id>)`. Emit it in the SAME
// transaction as the message insert so the tail can never diverge from the row.
type NotifyPublisher struct {
	pool *pgxpool.Pool
}

// NewNotifyPublisher builds a NotifyPublisher over the pool.
func NewNotifyPublisher(pool *pgxpool.Pool) *NotifyPublisher {
	return &NotifyPublisher{pool: pool}
}

// Publish sends the id over Channel (≤8KB payload — id only, hydrate from the
// row). Callers running inside the message-insert transaction should route the
// pg_notify through that same tx so the notification and the row commit atomically.
func (p *NotifyPublisher) Publish(ctx context.Context, messageID int64) error {
	_, err := p.pool.Exec(ctx, `SELECT pg_notify($1, $2)`,
		Channel, strconv.FormatInt(messageID, 10))
	return err
}
