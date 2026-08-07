# telegram — transport on the bus (UTF-16 splitter, allowlist, inbound→row)

## Unit
telegram

## Package / Owned files
`internal/telegram/*.go`

## Deps
bus

## Tier
standard

## Interfaces
```go
package telegram

import (
	"context"
	"net/http"

	"github.com/giantbeaver9/yuno/internal/bus"
)

const maxUnits = 4096 // Telegram's limit, measured in UTF-16 code units

// Update is the subset of the Bot API getUpdates payload we consume.
type Update struct {
	UpdateID int64
	ChatID   int64
	Text     string
	IsPrivate bool // true only for 1:1 chats (ignore groups/channels)
}

// Client is the raw Bot API client (no library), long-poll + offset ack.
type Client struct {
	/* token string; http *http.Client; base string */
}

func NewClient(token string, hc *http.Client) *Client
func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]Update, error)
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error // chunks via SplitMessage

// SplitMessage splits s into chunks each ≤ maxUnits UTF-16 code units (astral
// emoji = 2 units), preferring to cut on the last '\n' within the window; a
// single over-long line with no newline is hard-split at the unit boundary.
// Measured with unicode/utf16, NOT len() (bytes) or rune count (PORT.md gotcha ①).
func SplitMessage(s string) []string

// Allowed reports whether chatID is permitted. Empty allow = discovery mode
// (accept any private chat); non-empty = strict allowlist.
func Allowed(chatID int64, allow []int64) bool

// InboundToEnqueue maps a private text Update to a bus enqueue with
// from_ref="human:telegram:<chatID>", to_ref="agent:<boundAgentID>". Returns
// (_, false) for non-private or non-text updates. RunID is filled by the caller
// from the bound run.
func InboundToEnqueue(u Update, boundAgentID int64) (bus.EnqueueParams, bool)

// Redact strips the bot token from an error string (a failed http.Do returns a
// *url.Error embedding the token-bearing URL — PORT.md gotcha ②).
func Redact(token, s string) string

// Bot ties the pieces together: long-poll → Allowed → InboundToEnqueue → bus.Enqueue,
// and posts agent replies back via SendMessage.
type Bot struct {
	/* client *Client; bus *bus.Bus; allow []int64; boundAgentID int64; runID string */
}

func NewBot(client *Client, b *bus.Bus, allow []int64, boundAgentID int64, runID string) *Bot
func (b *Bot) Poll(ctx context.Context) error
```

## Accept
- `SplitMessage` never returns a chunk exceeding `maxUnits` UTF-16 code units, prefers `\n` cut points, and its chunks rejoin to the original text.
- `Allowed` enforces the allowlist (and treats an empty list as discovery mode).
- `InboundToEnqueue` maps a private text update to `from_ref="human:telegram:<chatid>"`, `to_ref="agent:<id>"`, `content=text`; rejects non-private / non-text updates.
- `Redact` removes the token from any error string before logging.
- HTTP is faked in tests (no real Bot API calls).

## Test cases
- **[positive]** `SplitMessage` of a 5000-char ASCII string → each chunk ≤ 4096 units and the concatenation equals the input; `Allowed(123, []int64{123})` → `true`; `InboundToEnqueue({ChatID:555,Text:"hi",IsPrivate:true}, 7)` → `EnqueueParams{FromRef:"human:telegram:555", ToRef:"agent:7", Content:"hi"}, true`.
- **[negative]** `Allowed(999, []int64{123})` → `false`; `InboundToEnqueue` on a group/non-text update → `(_, false)`.
- **[edge]** `SplitMessage` of a string of astral emoji (2 UTF-16 units each) keeps every chunk ≤ 4096 units where a naive rune count would overshoot; a single 5000-char line with no `\n` is hard-split into ≤4096-unit pieces.

## Salvage
PORT `salvage/pi-server/reference/telegram/bot.go`: LIFT the raw Bot API `call`/wire types (bot.go:66-83,242-270), long-poll + offset ack (86-128), chat_id extraction (71-77), token redaction (242-278), typing indicator (178); PORT the allowlist/discovery (49-62,136-147) and `splitMessage` (220-299) **fixing the UTF-16 measurement** (telegram/PORT.md — measure `utf16.Encode([]rune(s))`, not runes). SUPERSEDED: in-memory history, LLM client.
