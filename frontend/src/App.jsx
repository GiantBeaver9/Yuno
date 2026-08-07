import React, { useEffect, useState } from "react";

// Minimal app shell. The graded screens — agents list, Factory "new agent"
// form, and the live SSE monitor — mount here as their API contracts land.
export default function App() {
  const [health, setHealth] = useState("checking…");

  useEffect(() => {
    fetch("/api/health")
      .then((r) => r.json())
      .then((d) => setHealth(d.status ?? "unknown"))
      .catch(() => setHealth("unreachable"));
  }, []);

  return (
    <main style={{ fontFamily: "system-ui, sans-serif", maxWidth: 820, margin: "3rem auto", padding: "0 1rem" }}>
      <h1>Yuno</h1>
      <p style={{ color: "#666" }}>
        Agent orchestration platform — create agents, wire them into workflows, watch them run.
      </p>
      <p>
        Backend health: <strong>{health}</strong>
      </p>
      <section style={{ marginTop: "2rem", display: "grid", gap: "1rem" }}>
        <Placeholder title="Agents" desc="Per-agent provider, model, tools, guardrails, and roles." />
        <Placeholder title="Factory" desc="Hand it a key + description; it brings a new agent online live." />
        <Placeholder title="Live monitor" desc="SSE tail of the message bus — every turn, gap-free." />
      </section>
    </main>
  );
}

function Placeholder({ title, desc }) {
  return (
    <div style={{ border: "1px solid #ddd", borderRadius: 8, padding: "1rem" }}>
      <h2 style={{ margin: "0 0 .25rem" }}>{title}</h2>
      <p style={{ margin: 0, color: "#666" }}>{desc}</p>
    </div>
  );
}
