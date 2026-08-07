package runner

import (
	"context"
	"errors"
	"testing"
)

// --- ParseDecision ---

func TestParseDecisionApproveWithSummary(t *testing.T) {
	// [positive] well-formed stdout with trailing DECISION/SUMMARY lines
	stdout := "did some work\nchecked things\nDECISION: approve\nSUMMARY: looks good\n"
	decision, summary, err := ParseDecision(stdout)
	if err != nil {
		t.Fatalf("ParseDecision returned unexpected error: %v", err)
	}
	if decision != "approve" {
		t.Fatalf("expected decision %q, got %q", "approve", decision)
	}
	if summary != "looks good" {
		t.Fatalf("expected summary %q, got %q", "looks good", summary)
	}
}

func TestFakeRunnerReturnsScriptedResultInOrder(t *testing.T) {
	// [positive] FakeRunner{Results:[{Decision:"complete"}]} returns that result on first Run
	fr := &FakeRunner{Results: []TurnResult{{Decision: "complete"}}}
	res, err := fr.Run(context.Background(), TurnRequest{})
	if err != nil {
		t.Fatalf("FakeRunner.Run returned unexpected error: %v", err)
	}
	if res.Decision != "complete" {
		t.Fatalf("expected decision %q, got %q", "complete", res.Decision)
	}
}

func TestFakeRunnerReturnsResultsInSequence(t *testing.T) {
	// [positive] FakeRunner advances its cursor across multiple Run calls
	fr := &FakeRunner{Results: []TurnResult{
		{Decision: "approve"},
		{Decision: "reject"},
	}}
	first, err := fr.Run(context.Background(), TurnRequest{})
	if err != nil {
		t.Fatalf("unexpected error on first Run: %v", err)
	}
	if first.Decision != "approve" {
		t.Fatalf("expected first decision %q, got %q", "approve", first.Decision)
	}
	second, err := fr.Run(context.Background(), TurnRequest{})
	if err != nil {
		t.Fatalf("unexpected error on second Run: %v", err)
	}
	if second.Decision != "reject" {
		t.Fatalf("expected second decision %q, got %q", "reject", second.Decision)
	}
}

func TestFakeRunnerSurfacesErr(t *testing.T) {
	// [positive] FakeRunner surfaces its scripted Err
	wantErr := errors.New("boom")
	fr := &FakeRunner{Err: wantErr}
	_, err := fr.Run(context.Background(), TurnRequest{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected FakeRunner.Run to surface %v, got %v", wantErr, err)
	}
}

// --- ParseDecision negative cases ---

func TestParseDecisionInvalidEnum(t *testing.T) {
	// [negative] DECISION: maybe is not a valid enum value
	stdout := "some output\nDECISION: maybe\nSUMMARY: unclear\n"
	_, _, err := ParseDecision(stdout)
	if !errors.Is(err, ErrNoDecision) {
		t.Fatalf("expected ErrNoDecision for invalid enum, got %v", err)
	}
}

func TestParseDecisionNoDecisionLine(t *testing.T) {
	// [negative] no DECISION: line at all
	stdout := "just some plain agent chatter with no verdict at all\n"
	_, _, err := ParseDecision(stdout)
	if !errors.Is(err, ErrNoDecision) {
		t.Fatalf("expected ErrNoDecision when no DECISION line present, got %v", err)
	}
}

// --- ParseDecision edge cases ---

func TestParseDecisionMultipleLinesLastWins(t *testing.T) {
	// [edge] two DECISION lines: reject then approve -> last one wins
	stdout := "DECISION: reject\nSUMMARY: initial concerns\nmore work happened\nDECISION: approve\nSUMMARY: resolved\n"
	decision, summary, err := ParseDecision(stdout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != "approve" {
		t.Fatalf("expected last decision %q to win, got %q", "approve", decision)
	}
	if summary != "resolved" {
		t.Fatalf("expected summary %q, got %q", "resolved", summary)
	}
}

func TestParseDecisionCaseInsensitiveUppercase(t *testing.T) {
	// [edge] DECISION: COMPLETE in uppercase is accepted and normalized to lowercase
	stdout := "DECISION: COMPLETE\nSUMMARY: all done\n"
	decision, summary, err := ParseDecision(stdout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != "complete" {
		t.Fatalf("expected normalized decision %q, got %q", "complete", decision)
	}
	if summary != "all done" {
		t.Fatalf("expected summary %q, got %q", "all done", summary)
	}
}

func TestParseDecisionMissingSummary(t *testing.T) {
	// [edge] missing SUMMARY line -> summary is "" with no error
	stdout := "DECISION: reject\n"
	decision, summary, err := ParseDecision(stdout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != "reject" {
		t.Fatalf("expected decision %q, got %q", "reject", decision)
	}
	if summary != "" {
		t.Fatalf("expected empty summary, got %q", summary)
	}
}

func TestParseDecisionEmptyOutput(t *testing.T) {
	// [edge] empty output handled without panic -> ErrNoDecision
	decision, summary, err := ParseDecision("")
	if !errors.Is(err, ErrNoDecision) {
		t.Fatalf("expected ErrNoDecision on empty output, got %v", err)
	}
	if decision != "" || summary != "" {
		t.Fatalf("expected empty decision/summary on error, got (%q,%q)", decision, summary)
	}
}

func TestParseDecisionExtraWhitespace(t *testing.T) {
	// [edge] extra whitespace around DECISION/SUMMARY values is trimmed
	stdout := "DECISION:   approve   \nSUMMARY:    padded summary   \n"
	decision, summary, err := ParseDecision(stdout)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != "approve" {
		t.Fatalf("expected decision %q, got %q", "approve", decision)
	}
	if summary != "padded summary" {
		t.Fatalf("expected trimmed summary %q, got %q", "padded summary", summary)
	}
}
