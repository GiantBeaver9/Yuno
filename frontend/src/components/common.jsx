import React from "react";

export function ErrorLine({ message }) {
  if (!message) return null;
  return <div className="error-line">{message}</div>;
}

export function EmptyLine({ children }) {
  return <div className="empty-line">{children}</div>;
}

export function Pill({ tone = "neutral", children }) {
  return <span className={`pill pill-${tone}`}>{children}</span>;
}

// Maps a run/message status string to a Pill tone.
export function statusTone(status) {
  const s = (status || "").toLowerCase();
  if (s.includes("error") || s.includes("fail") || s.includes("blocked")) return "err";
  if (s.includes("needs_human") || s.includes("pending") || s.includes("wait")) return "warn";
  if (s.includes("done") || s.includes("complete") || s.includes("ok") || s.includes("approved")) return "ok";
  if (s.includes("running")) return "accent";
  return "neutral";
}

export function timeNow() {
  return new Date().toLocaleTimeString();
}
