import React, { useEffect, useState } from "react";
import "./App.css";
import { api } from "./api.js";
import AgentsTab from "./components/AgentsTab.jsx";
import WorkflowsTab from "./components/WorkflowsTab.jsx";
import RunsTab from "./components/RunsTab.jsx";
import MonitorTab from "./components/MonitorTab.jsx";

const TABS = [
  { key: "agents", label: "Agents", render: () => <AgentsTab /> },
  { key: "workflows", label: "Workflows", render: () => <WorkflowsTab /> },
  { key: "runs", label: "Runs", render: () => <RunsTab /> },
  { key: "monitor", label: "Live monitor", render: () => <MonitorTab /> },
];

export default function App() {
  const [health, setHealth] = useState("checking");
  const [active, setActive] = useState("agents");

  useEffect(() => {
    let cancelled = false;
    function checkHealth() {
      api
        .health()
        .then((d) => !cancelled && setHealth(d?.status || "unknown"))
        .catch(() => !cancelled && setHealth("unreachable"));
    }
    checkHealth();
    const t = setInterval(checkHealth, 10000);
    return () => {
      cancelled = true;
      clearInterval(t);
    };
  }, []);

  const healthDot = health === "ok" ? "ok" : health === "checking" ? "pending" : "bad";

  return (
    <div className="app">
      <header className="app-header">
        <div>
          <h1>Yuno</h1>
          <p className="tagline">Agent orchestration — create agents, wire workflows, watch them run.</p>
        </div>
        <span className="health-badge">
          <span className={`dot ${healthDot}`} />
          backend: {health}
        </span>
      </header>

      <nav className="tabs">
        {TABS.map((t) => (
          <button
            key={t.key}
            className={`tab-btn${active === t.key ? " active" : ""}`}
            onClick={() => setActive(t.key)}
          >
            {t.label}
          </button>
        ))}
      </nav>

      <main>{TABS.find((t) => t.key === active)?.render()}</main>
    </div>
  );
}
