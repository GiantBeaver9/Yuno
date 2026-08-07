package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// defaultBase is the real Bot API host. Tests override Client.base to point
// at an httptest.Server so no real Telegram API call is ever made.
const defaultBase = "https://api.telegram.org"

// Client is the raw Bot API client (no library), long-poll + offset ack.
type Client struct {
	token string
	http  *http.Client
	base  string
}

// NewClient builds a Client for the given bot token. If hc is nil a client
// with a timeout longer than the long-poll window is used.
func NewClient(token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 65 * time.Second} // > long-poll timeout
	}
	return &Client{token: token, http: hc, base: defaultBase}
}

// --- Telegram wire types (subset) ---

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	Text string `json:"text"`
	Chat struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
}

type tgResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

// GetUpdates issues one long-poll (timeout=30s) starting at offset, returning
// only message updates (non-message updates, e.g. edited_message, are
// skipped).
func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	raw, err := c.call(ctx, "getUpdates", map[string]any{"timeout": 30, "offset": offset})
	if err != nil {
		return nil, err
	}
	var raws []tgUpdate
	if err := json.Unmarshal(raw, &raws); err != nil {
		return nil, fmt.Errorf("telegram getUpdates: decode: %s", Redact(c.token, err.Error()))
	}
	updates := make([]Update, 0, len(raws))
	for _, u := range raws {
		if u.Message == nil {
			continue
		}
		updates = append(updates, Update{
			UpdateID:  u.UpdateID,
			ChatID:    u.Message.Chat.ID,
			Text:      u.Message.Text,
			IsPrivate: u.Message.Chat.Type == "private",
		})
	}
	return updates, nil
}

// SendMessage delivers text to a chat, chunking via SplitMessage so no single
// call exceeds Telegram's UTF-16 length limit.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	for _, chunk := range SplitMessage(text) {
		payload := map[string]any{"chat_id": chatID, "text": chunk}
		if _, err := c.call(ctx, "sendMessage", payload); err != nil {
			return err
		}
	}
	return nil
}

// call performs one Bot API method POST and returns its `result` payload. Any
// error is redacted before it can leak the token: a failed http.Do returns a
// *url.Error embedding the token-bearing request URL.
func (c *Client) call(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("telegram %s: marshal: %w", method, err)
	}
	url := fmt.Sprintf("%s/bot%s/%s", c.base, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("telegram %s: %s", method, Redact(c.token, err.Error()))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("telegram %s: %s", method, Redact(c.token, err.Error()))
	}
	defer resp.Body.Close()

	var tr tgResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("telegram %s: decode: %s", method, Redact(c.token, err.Error()))
	}
	if !tr.OK {
		return nil, fmt.Errorf("telegram %s: %s", method, Redact(c.token, tr.Description))
	}
	return tr.Result, nil
}
