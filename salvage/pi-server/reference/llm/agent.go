// Package llm implements a small OpenAI-compatible tool-calling agent over the
// local LLM (LM Studio / Ollama). It exposes the SQLite store (calendar, todos,
// stats) as tools the model can call, so both the web chat panel and the
// Telegram bot can drive real actions through natural language.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/giantbeaver9/pi-home-server-page/backend/internal/db"
)

// Message is one entry in an OpenAI chat conversation. Content is omitted from
// the wire when empty (tool-call turns carry no text) and, on the way in,
// tolerates a null content some servers send by decoding to "".
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a function the model asked us to run; Arguments is a JSON-encoded
// object we unmarshal per-tool.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// UnmarshalJSON tolerates servers that send `content: null` on tool-call turns
// by mapping it to an empty string.
func (m *Message) UnmarshalJSON(data []byte) error {
	type alias Message // avoid recursion
	aux := struct {
		Content *string `json:"content"`
		*alias
	}{alias: (*alias)(m)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.Content != nil {
		m.Content = *aux.Content
	} else {
		m.Content = ""
	}
	return nil
}

// Agent is a stateless (aside from config) tool-calling driver over the store.
// A single instance is safe to share between the HTTP endpoint and the bot.
type Agent struct {
	baseURL string
	model   string
	store   *db.Store
	http    *http.Client
}

// NewAgent builds an agent against an OpenAI-compatible base URL (e.g.
// http://localhost:1234/v1). model may be "" — LM Studio then uses whatever
// model is currently loaded.
func NewAgent(baseURL, model string, store *db.Store) *Agent {
	return &Agent{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		store:   store,
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

const maxIterations = 6

// chatRequest / chatResponse mirror the subset of the OpenAI chat-completions
// schema we use.
type chatRequest struct {
	Model      string    `json:"model,omitempty"`
	Messages   []Message `json:"messages"`
	Tools      []any     `json:"tools,omitempty"`
	ToolChoice string    `json:"tool_choice,omitempty"`
	Stream     bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error json.RawMessage `json:"error,omitempty"`
}

// Run executes one assistant turn over the prior conversation msgs (roles
// user/assistant/tool, NO system message — Run injects a fresh one carrying the
// current local time each turn). It runs the tool-calling loop, executing any
// tool calls against the store, up to a bounded number of iterations. It
// returns the final assistant text plus the messages generated this turn
// (assistant tool-call messages, tool result messages, and the final assistant
// message) so callers that keep history can append them.
// owner scopes per-user tools (projects, check-ins, goals) — the Telegram chat
// id, or 0 for the shared web context. Shared tools (calendar/todos/stats)
// ignore it.
func (a *Agent) Run(ctx context.Context, msgs []Message, owner int64) (reply string, appended []Message, err error) {
	// Fresh system prompt each turn so the model always has an accurate clock.
	convo := make([]Message, 0, len(msgs)+4)
	convo = append(convo, a.systemMessage())
	convo = append(convo, msgs...)

	tools := toolSchemas()

	for i := 0; i < maxIterations; i++ {
		resp, err := a.complete(ctx, convo, tools)
		if err != nil {
			return "", appended, err
		}
		if len(resp.Choices) == 0 {
			return "", appended, fmt.Errorf("llm returned no choices")
		}
		msg := resp.Choices[0].Message
		msg.Role = "assistant" // some servers omit role on the choice message

		// No tool calls → this is the final answer.
		if len(msg.ToolCalls) == 0 {
			appended = append(appended, msg)
			return msg.Content, appended, nil
		}

		// Record the assistant tool-call turn, then run each call.
		convo = append(convo, msg)
		appended = append(appended, msg)
		for _, tc := range msg.ToolCalls {
			result, derr := a.dispatch(tc.Function.Name, tc.Function.Arguments, owner)
			var content string
			if derr != nil {
				b, _ := json.Marshal(map[string]string{"error": derr.Error()})
				content = string(b)
			} else {
				b, merr := json.Marshal(result)
				if merr != nil {
					b, _ = json.Marshal(map[string]string{"error": merr.Error()})
				}
				content = string(b)
			}
			toolMsg := Message{Role: "tool", ToolCallID: tc.ID, Name: tc.Function.Name, Content: content}
			convo = append(convo, toolMsg)
			appended = append(appended, toolMsg)
		}
	}

	// Iteration cap hit without a plain-text answer: surface a graceful note.
	note := Message{Role: "assistant", Content: "Sorry — I couldn't finish that in the allotted steps. Please try rephrasing."}
	appended = append(appended, note)
	return note.Content, appended, nil
}

// complete POSTs one chat-completions request and decodes the response.
func (a *Agent) complete(ctx context.Context, msgs []Message, tools []any) (*chatResponse, error) {
	reqBody := chatRequest{
		Model:      a.model, // omitted from JSON when "" (LM Studio uses loaded model)
		Messages:   msgs,
		Tools:      tools,
		ToolChoice: "auto",
		Stream:     false,
	}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := a.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm upstream: %w", err)
	}
	defer resp.Body.Close()

	var out chatResponse
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("llm decode: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("llm status %d: %s", resp.StatusCode, string(out.Error))
	}
	return &out, nil
}

// systemMessage establishes the assistant's role and grounds it in the current
// local time so it can resolve relative dates deterministically.
func (a *Agent) systemMessage() Message {
	now := time.Now()
	content := fmt.Sprintf(
		"You are the assistant for a private home-server intranet. "+
			"The current local time is %s (%s), timezone %s. "+
			"Use the provided tools for any calendar, todo, server-stats, project-tracking, "+
			"self-check-in, or goal action rather than guessing. "+
			"Resolve relative dates like 'tomorrow' or 'Thursday 3pm' against the current time above. "+
			"Keep replies concise and plain-text, friendly for Telegram — avoid markdown tables and heavy formatting. "+
			"After making a calendar or todo change, briefly confirm what you did.",
		now.Format("2006-01-02 15:04"), now.Format("Monday"), timezoneName(now),
	)
	return Message{Role: "system", Content: content}
}

// timezoneName reports the IANA/local zone name for the given time.
func timezoneName(t time.Time) string {
	name := time.Local.String()
	if name == "" || name == "Local" {
		z, _ := t.Zone()
		return z
	}
	return name
}
