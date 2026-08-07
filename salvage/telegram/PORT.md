# Telegram salvage — port map

Source: `github.com/GiantBeaver9/Anticode` @ branch `claude/telegram-bot-local-llm-3tiohp`
(commit `97839bd`). Frozen copies in `reference/`.

**Verdict:** small personal Python bot (Telegram → local LLM via polling) + an Azure
Functions cron digest. No Go to lift. The value is a few sharp-edge *patterns* to port; most
of the bot is superseded by the Yuno architecture (see §refs to `ARCHITECTURE.md`).

Port only what's marked **PORT**. Skip everything marked **SUPERSEDED** — porting it would
fight the design.

| Pattern | Source | Verdict | Go destination |
|---|---|---|---|
| **UTF-16 message splitting** (4096 limit is UTF-16 code units, emoji = 2, split on `\n`) | `bot.py:104-122`, `daily_digest_function.py:28-34` | **PORT** — the gem | Outbound side of Telegram transport (§13). Use `unicode/utf16` to measure; chunk on newline boundaries. |
| **Allowlist + private-only auth** (respond only to allowlisted user IDs, 1:1 chats, ignore groups/channels) | `bot.py:51-65` | **PORT** | Inbound guard in Telegram transport before writing the bus row. |
| **Raw Bot API outbound** (`POST api.telegram.org/bot<token>/sendMessage`, chunked) | `daily_digest_function.py:22-34` | **PORT** | Library-free outbound reference; or use as the send path directly. |
| **Typing indicator** while the agent works (`send_chat_action TYPING`) | `bot.py:150` | **NICE-TO-HAVE** | Emit typing on the bound chat when a turn is spawned for it. |
| **Polling skeleton** (long-poll getUpdates; filter private + text + non-command) | `bot.py:176-188` | **REFERENCE** | Pattern only — in Go use a telegram lib (e.g. `go-telegram-bot-api` / `telebot`). |
| **Model auto-detect via `/models`** | `bot.py:68-82` | **SKIP** | Goose owns the model call. Keep only if you ever bypass Goose. |
| **In-memory chat history + trim-in-pairs** | `bot.py:47-48,142-170` | **SUPERSEDED** | Yuno keeps the trail in the bus/DB keyed by `guid` (§8). Do **not** port. |
| **OpenAI-compatible LLM client** | `bot.py:85-101` | **SUPERSEDED** | Goose calls the model (Gemini/HF). Do **not** port. |
| **Azure Functions cron hosting** | `daily_digest_function.py:45-49` | **SUPERSEDED** | Yuno uses an in-process ticker goroutine (§16). The *trigger concept* maps to schedule → bus; the Azure hosting does not port. |

## Env mapping

The bot's `.env` (`reference/` shows the shape) maps into Yuno's Telegram transport config:

- `TELEGRAM_BOT_TOKEN` → transport credential (platform-level or per-bound-agent)
- `ALLOWED_USER_IDS` → the inbound allowlist guard
- `LLM_*` → **dropped** — Goose + per-agent `provider_key` replace all of it

## The UTF-16 gotcha, spelled out (so you don't relearn it)

Telegram's 4096-char limit is measured in **UTF-16 code units**, not Unicode codepoints or
bytes. Emoji and other astral-plane characters are 2 units each. Naive `len(string)` (Go
`len()` counts bytes; `utf8.RuneCountInString` counts codepoints) will let a message through
that Telegram then rejects. Measure with `utf16.Encode([]rune(s))` length, chunk to <= 4096
units, prefer cutting on the last `\n` in the window. Both Python files already do this
correctly — mirror their logic.
