// Package telegram implements the Telegram transport for the Yuno bus: a
// library-free Bot API client (long-poll + offset ack), an allowlist/private-
// only inbound guard, and a UTF-16-aware message splitter for outbound sends.
// Inbound private messages from allowlisted chats become bus rows via
// bus.Bus.Enqueue with FromRef "human:telegram:<chatid>"; nothing else is
// written to the bus. See docs/telegram.md.
package telegram

import (
	"strings"
	"unicode/utf16"
)

const maxUnits = 4096 // Telegram's limit, measured in UTF-16 code units

// Update is the subset of the Bot API getUpdates payload we consume.
type Update struct {
	UpdateID  int64
	ChatID    int64
	Text      string
	IsPrivate bool // true only for 1:1 chats (ignore groups/channels)
}

// isHighSurrogate reports whether u is the first unit of a UTF-16 surrogate
// pair (used to keep an astral-plane rune's two units from being split
// across chunks).
func isHighSurrogate(u uint16) bool {
	return u >= 0xD800 && u <= 0xDBFF
}

// SplitMessage splits s into chunks each ≤ maxUnits UTF-16 code units (astral
// emoji = 2 units), preferring to cut on the last '\n' within the window; a
// single over-long line with no newline is hard-split at the unit boundary.
// Measured with unicode/utf16, NOT len() (bytes) or rune count (PORT.md gotcha ①).
func SplitMessage(s string) []string {
	if s == "" {
		return []string{""}
	}

	units := utf16.Encode([]rune(s))
	if len(units) <= maxUnits {
		return []string{s}
	}

	const newlineUnit = uint16('\n') // U+000A is always a single BMP unit

	var chunks []string
	for len(units) > maxUnits {
		cut := maxUnits

		// Prefer the last '\n' within the window.
		for i := cut - 1; i >= 0; i-- {
			if units[i] == newlineUnit {
				cut = i + 1
				break
			}
		}

		// Never split a surrogate pair across chunks: if the unit just
		// before the cut is a high surrogate, its low surrogate partner is
		// at units[cut] — pull the cut back one unit so the pair stays
		// together in the next chunk.
		if cut > 0 && cut < len(units) && isHighSurrogate(units[cut-1]) {
			cut--
		}
		if cut <= 0 {
			// Degenerate guard (shouldn't happen given maxUnits >> 1).
			cut = 1
		}

		chunks = append(chunks, string(utf16.Decode(units[:cut])))
		units = units[cut:]
	}
	if len(units) > 0 {
		chunks = append(chunks, string(utf16.Decode(units)))
	}
	return chunks
}

// Allowed reports whether chatID is permitted. Empty allow = discovery mode
// (accept any private chat); non-empty = strict allowlist.
func Allowed(chatID int64, allow []int64) bool {
	if len(allow) == 0 {
		return true
	}
	for _, id := range allow {
		if id == chatID {
			return true
		}
	}
	return false
}

// Redact strips the bot token from an error string (a failed http.Do returns a
// *url.Error embedding the token-bearing URL — PORT.md gotcha ②).
func Redact(token, s string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "***")
}
