package telegram

import (
	"context"
	"fmt"
	"strings"

	"github.com/giantbeaver9/yuno/internal/bus"
)

// InboundToEnqueue maps a private text Update to a bus enqueue with
// from_ref="human:telegram:<chatID>", to_ref="agent:<boundAgentID>". Returns
// (_, false) for non-private or non-text updates. RunID is filled by the caller
// from the bound run.
func InboundToEnqueue(u Update, boundAgentID int64) (bus.EnqueueParams, bool) {
	if !u.IsPrivate || strings.TrimSpace(u.Text) == "" {
		return bus.EnqueueParams{}, false
	}
	return bus.EnqueueParams{
		FromRef: fmt.Sprintf("human:telegram:%d", u.ChatID),
		ToRef:   fmt.Sprintf("agent:%d", boundAgentID),
		Content: u.Text,
	}, true
}

// Bot ties the pieces together: long-poll → Allowed → InboundToEnqueue → bus.Enqueue,
// and posts agent replies back via SendMessage.
type Bot struct {
	client       *Client
	bus          *bus.Bus
	allow        []int64
	boundAgentID int64
	runID        string
}

// NewBot builds a Bot bound to one agent and one run.
func NewBot(client *Client, b *bus.Bus, allow []int64, boundAgentID int64, runID string) *Bot {
	return &Bot{
		client:       client,
		bus:          b,
		allow:        allow,
		boundAgentID: boundAgentID,
		runID:        runID,
	}
}

// Poll performs one getUpdates round-trip and, for every update that passes
// the allowlist and is a private text message, enqueues it onto the bus
// bound run. Updates from disallowed chats, groups/channels, or without text
// are silently dropped (no bus row is written). Callers wanting continuous
// long-polling call Poll in a loop, tracking their own offset via the update
// count/backoff policy they need — kept thin here so the pure pieces
// (SplitMessage, Allowed, InboundToEnqueue, Redact) carry the tested logic.
func (b *Bot) Poll(ctx context.Context) error {
	updates, err := b.client.GetUpdates(ctx, 0)
	if err != nil {
		return err
	}
	for _, u := range updates {
		if !Allowed(u.ChatID, b.allow) {
			continue
		}
		params, ok := InboundToEnqueue(u, b.boundAgentID)
		if !ok {
			continue
		}
		params.RunID = b.runID
		if _, err := b.bus.Enqueue(ctx, params); err != nil {
			return err
		}
	}
	return nil
}
