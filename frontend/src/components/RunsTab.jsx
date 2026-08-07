import React, { useEffect, useRef, useState } from "react";
import { api } from "../api.js";
import { ErrorLine, EmptyLine, Pill, statusTone } from "./common.jsx";

const POLL_MS = 2000;

export default function RunsTab() {
  const [workflows, setWorkflows] = useState([]);
  const [runs, setRuns] = useState([]);
  const [runsError, setRunsError] = useState("");
  const [loading, setLoading] = useState(true);

  const [selectedGuid, setSelectedGuid] = useState(null);
  const [messages, setMessages] = useState([]);
  const [messagesError, setMessagesError] = useState("");

  const [form, setForm] = useState({ workflowId: "", input: "", maxIterations: 10 });
  const [starting, setStarting] = useState(false);
  const [startError, setStartError] = useState("");

  const [resuming, setResuming] = useState(false);
  const [resumeError, setResumeError] = useState("");

  const selectedGuidRef = useRef(selectedGuid);
  selectedGuidRef.current = selectedGuid;

  async function loadRuns() {
    try {
      const data = await api.listRuns();
      setRuns(data || []);
      setRunsError("");
    } catch (err) {
      setRunsError(err.message);
    } finally {
      setLoading(false);
    }
  }

  async function loadMessages(guid) {
    try {
      const data = await api.getRunMessages(guid);
      if (selectedGuidRef.current === guid) {
        setMessages(data || []);
        setMessagesError("");
      }
    } catch (err) {
      if (selectedGuidRef.current === guid) setMessagesError(err.message);
    }
  }

  // Initial workflow list (for the start-run picker) + first runs load.
  useEffect(() => {
    api
      .listWorkflows()
      .then((data) => {
        setWorkflows(data || []);
        if (data && data.length > 0) {
          setForm((f) => ({ ...f, workflowId: String(data[0].id) }));
        }
      })
      .catch(() => {});
    loadRuns();
  }, []);

  // Poll the run list every ~2s.
  useEffect(() => {
    const t = setInterval(loadRuns, POLL_MS);
    return () => clearInterval(t);
  }, []);

  // Poll the selected run's messages every ~2s.
  useEffect(() => {
    if (!selectedGuid) {
      setMessages([]);
      return;
    }
    loadMessages(selectedGuid);
    const t = setInterval(() => loadMessages(selectedGuid), POLL_MS);
    return () => clearInterval(t);
  }, [selectedGuid]);

  async function handleStart(e) {
    e.preventDefault();
    setStartError("");
    if (!form.workflowId) {
      setStartError("Pick a workflow.");
      return;
    }
    setStarting(true);
    try {
      const created = await api.createRun({
        workflowId: Number(form.workflowId),
        input: form.input,
        maxIterations: Number(form.maxIterations) || 10,
      });
      setForm((f) => ({ ...f, input: "" }));
      await loadRuns();
      if (created && created.guid) setSelectedGuid(created.guid);
    } catch (err) {
      setStartError(err.message);
    } finally {
      setStarting(false);
    }
  }

  async function handleResume() {
    if (!selectedGuid) return;
    setResumeError("");
    setResuming(true);
    try {
      await api.resumeRun(selectedGuid);
      await loadRuns();
      await loadMessages(selectedGuid);
    } catch (err) {
      setResumeError(err.message);
    } finally {
      setResuming(false);
    }
  }

  const selectedRun = runs.find((r) => r.guid === selectedGuid);

  return (
    <div className="stack">
      <div className="panel">
        <h2>Start run</h2>
        <ErrorLine message={startError} />
        <form className="form-grid" onSubmit={handleStart}>
          <div className="field-row">
            <div>
              <label htmlFor="run-workflow">Workflow</label>
              <select
                id="run-workflow"
                value={form.workflowId}
                onChange={(e) => setForm((f) => ({ ...f, workflowId: e.target.value }))}
              >
                {workflows.length === 0 && <option value="">no workflows available</option>}
                {workflows.map((w) => (
                  <option key={w.id} value={w.id}>
                    {w.name} (#{w.id})
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label htmlFor="run-maxiter">Max iterations</label>
              <input
                id="run-maxiter"
                type="number"
                min="1"
                value={form.maxIterations}
                onChange={(e) => setForm((f) => ({ ...f, maxIterations: e.target.value }))}
              />
            </div>
          </div>
          <div>
            <label htmlFor="run-input">Input</label>
            <textarea
              id="run-input"
              value={form.input}
              onChange={(e) => setForm((f) => ({ ...f, input: e.target.value }))}
              placeholder="Kickoff message for the entry agent…"
            />
          </div>
          <div>
            <button className="btn btn-primary" type="submit" disabled={starting || workflows.length === 0}>
              {starting ? "Starting…" : "Start run"}
            </button>
          </div>
        </form>
      </div>

      <div className="two-col">
        <div className="panel">
          <div className="section-row">
            <h2>Runs</h2>
            <button className="btn btn-sm" onClick={loadRuns} disabled={loading}>
              Refresh
            </button>
          </div>
          <ErrorLine message={runsError} />
          {loading ? (
            <EmptyLine>Loading runs…</EmptyLine>
          ) : runs.length === 0 ? (
            <EmptyLine>No runs yet — start one above.</EmptyLine>
          ) : (
            <ul className="run-list">
              {runs.map((r) => (
                <li
                  key={r.guid}
                  className={r.guid === selectedGuid ? "selected" : ""}
                  onClick={() => setSelectedGuid(r.guid)}
                >
                  <div style={{ display: "flex", justifyContent: "space-between", gap: "0.5rem" }}>
                    <span className="run-guid">{r.guid.slice(0, 12)}…</span>
                    <Pill tone={statusTone(r.status)}>{r.status}</Pill>
                  </div>
                  <div className="muted" style={{ fontSize: "0.78rem", marginTop: "0.15rem" }}>
                    workflow #{r.workflowId} · iteration {r.iterations}/{r.maxIterations}
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="panel">
          <div className="section-row">
            <h2>Message trail</h2>
            {selectedRun && selectedRun.status === "needs_human" && (
              <button className="btn btn-primary btn-sm" onClick={handleResume} disabled={resuming}>
                {resuming ? "Resuming…" : "Resume"}
              </button>
            )}
          </div>
          <ErrorLine message={resumeError} />
          <ErrorLine message={messagesError} />
          {!selectedGuid ? (
            <EmptyLine>Select a run to see its message bus.</EmptyLine>
          ) : messages.length === 0 ? (
            <EmptyLine>No messages yet on this run.</EmptyLine>
          ) : (
            <div className="feed">
              {messages.map((m) => (
                <div className="feed-item" key={m.id}>
                  <div className="feed-head">
                    <span className="feed-refs">
                      {m.fromRef} → {m.toRef}
                    </span>
                    {m.decision && <Pill tone={statusTone(m.decision)}>{m.decision}</Pill>}
                    {m.status && <Pill tone={statusTone(m.status)}>{m.status}</Pill>}
                    {m.cost > 0 && <span className="muted">${m.cost.toFixed(4)}</span>}
                    {m.tokens > 0 && <span className="muted">{m.tokens} tok</span>}
                  </div>
                  <div className="feed-content">{m.content}</div>
                  {m.summary && <div className="feed-summary">{m.summary}</div>}
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
