# telegram — transport on the bus

The `telegram` package is a thin, library-free transport that fronts the bus
with Telegram's Bot API: it long-polls `getUpdates` over raw `net/http`,
filters to allowlisted 1:1 chats, and turns each surviving text message into a
`bus.EnqueueParams` written with `bus.Bus.Enqueue`. Outbound replies go back
through the same raw API, chunked to fit Telegram's per-message limit. There is
no external Telegram library and no in-memory chat history — the bus/DB trail
(keyed by `run_id`/`seq`) *is* the conversation history.

## The UTF-16 gotcha (fixed here)

Telegram's 4096-character message limit is measured in **UTF-16 code units**,
not bytes and not Unicode codepoints. Characters outside the Basic Multilingual
Plane — most emoji included — encode as a *surrogate pair*, i.e. 2 units for 1
rune. A splitter that measures with `len(s)` (bytes) or
`utf8.RuneCountInString` (codepoints) will let a message through that is well
under its own count yet over Telegram's real limit, and the API rejects it.

`SplitMessage` measures with `unicode/utf16.Encode`, not `len()` or a rune
count:

```go
units := utf16.Encode([]rune(s))
```

It then chunks so no chunk exceeds `maxUnits` (4096) UTF-16 units, preferring
to cut at the last `\n` within the window so paragraphs aren't broken
mid-line. When no newline is available (a single very long line), it hard-
splits at the unit boundary — but never *inside* a surrogate pair: if the cut
would land right after a high surrogate (0xD800–0xDBFF), the cut backs up one
unit so the pair stays whole in the next chunk. Every chunk sequence
round-trips: concatenating the chunks reproduces the original string exactly,
astral-plane characters included.

## Allowlist / discovery mode

`Allowed(chatID, allow)` gates which chats may write to the bus:

- **Non-empty `allow`** — strict allowlist; only listed chat IDs pass.
- **Empty `allow`** — discovery mode: any private chat is accepted. This lets
  an operator run the bot with no configured IDs yet, message it once from
  their own account, and read the chat ID back out (from the enqueued bus row)
  to populate the allowlist for the strict mode that follows.

`Allowed` only checks chat identity. Privacy (1:1 vs. group/channel) is a
separate check baked into `InboundToEnqueue`, so a chat ID being on the
allowlist never lets a group message through.

## Inbound → bus row

`InboundToEnqueue(u Update, boundAgentID int64)` maps one `Update` to
`bus.EnqueueParams`:

- Rejects (`_, false`) anything that isn't a private, non-empty text message —
  group/channel updates and non-text updates (empty `Text`) never reach the
  bus.
- On success: `FromRef = "human:telegram:<chatID>"`, `ToRef =
  "agent:<boundAgentID>"`, `Content = Text`. `RunID` is left for the caller to
  fill in from the run the bot is bound to (`Bot` does this before calling
  `bus.Bus.Enqueue`).

`Bot.Poll` composes the pipeline for one `getUpdates` round-trip: fetch
updates → `Allowed` → `InboundToEnqueue` → `bus.Bus.Enqueue`. Updates that fail
either gate are silently dropped — no bus row, no error — so a disallowed chat
or a group message leaves no trace on the bus.

## Token redaction

A failed `http.Do` surfaces as a `*url.Error` whose message embeds the full
request URL — which contains the bot token
(`https://api.telegram.org/bot<token>/getUpdates`). Every error path in
`Client.call` runs through `Redact(token, s)` before it's wrapped, so the token
substring never reaches a returned error (and therefore never reaches a log
line built from one).

## Testing without the real API

`Client` takes its base URL from an unexported `base` field (defaulted to
`https://api.telegram.org` by `NewClient`); tests in this package construct a
`Client` and override `base` to point at an `httptest.Server`, so the full
`Bot.Poll` pipeline — including the `bus.Bus.Enqueue` write — is exercised
against a fake Bot API and a real (per-test-schema) Postgres via
`testutil.NewDB`, with no network call ever reaching Telegram.
