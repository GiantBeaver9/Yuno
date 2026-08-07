import React, { useEffect, useState } from "react";
import { api, splitList } from "../api.js";
import { ErrorLine, EmptyLine, Pill } from "./common.jsx";

const emptyForm = {
  name: "",
  provider: "gemini",
  model: "",
  key: "",
  prompt: "",
  roles: "",
  mode: "auto",
  tools: "",
  blockedTools: "",
  maxCost: "",
  rateLimit: "",
};

export default function AgentsTab() {
  const [agents, setAgents] = useState([]);
  const [listError, setListError] = useState("");
  const [loading, setLoading] = useState(true);

  const [form, setForm] = useState(emptyForm);
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState("");
  const [formSuccess, setFormSuccess] = useState("");

  async function loadAgents() {
    try {
      const data = await api.listAgents();
      setAgents(data || []);
      setListError("");
    } catch (err) {
      setListError(err.message);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    loadAgents();
  }, []);

  function updateField(field, value) {
    setForm((f) => ({ ...f, [field]: value }));
  }

  async function handleSubmit(e) {
    e.preventDefault();
    setFormError("");
    setFormSuccess("");

    if (!form.name.trim()) {
      setFormError("Name is required.");
      return;
    }

    const body = {
      name: form.name.trim(),
      provider: form.provider,
      model: form.model.trim(),
      key: form.key,
      prompt: form.prompt,
      roles: splitList(form.roles),
      mode: form.mode || "auto",
      tools: splitList(form.tools),
      blockedTools: splitList(form.blockedTools),
      maxCost: form.maxCost === "" ? 0 : Number(form.maxCost),
      rateLimit: form.rateLimit === "" ? 0 : Number(form.rateLimit),
      guid: "",
    };

    setSubmitting(true);
    try {
      const created = await api.createAgent(body);
      setFormSuccess(`Agent "${created.Name || body.name}" created (id ${created.ID ?? "?"}).`);
      setForm(emptyForm);
      await loadAgents();
    } catch (err) {
      setFormError(err.message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="two-col">
      <div className="panel">
        <div className="section-row">
          <h2>Agents</h2>
          <button className="btn btn-sm" onClick={loadAgents} disabled={loading}>
            Refresh
          </button>
        </div>
        <ErrorLine message={listError} />
        {loading ? (
          <EmptyLine>Loading agents…</EmptyLine>
        ) : agents.length === 0 ? (
          <EmptyLine>No agents yet — create one with the Factory form.</EmptyLine>
        ) : (
          <div className="table-scroll">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Provider / model</th>
                  <th>Mode</th>
                  <th>Roles</th>
                </tr>
              </thead>
              <tbody>
                {agents.map((a) => (
                  <tr key={a.ID ?? a.Guid ?? a.Name}>
                    <td>
                      <strong>{a.Name}</strong>
                      <div className="muted" style={{ fontSize: "0.75rem" }}>
                        #{a.ID}
                      </div>
                    </td>
                    <td>
                      {a.Provider} <span className="muted">/ {a.Model || "—"}</span>
                    </td>
                    <td>
                      <Pill tone={a.Mode === "approval" ? "warn" : "ok"}>{a.Mode || "auto"}</Pill>
                    </td>
                    <td>
                      {(a.Roles || []).length === 0 ? (
                        <span className="muted">—</span>
                      ) : (
                        a.Roles.map((r) => (
                          <span className="role-pill" key={r}>
                            {r}
                          </span>
                        ))
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="panel">
        <h2>New agent (Factory)</h2>
        <p className="caption">
          This form posts to the same <code>POST /api/agents</code> endpoint the Factory MCP tool calls —
          there is exactly one code path for bringing a new agent online, whether a human fills this in or
          an agent invokes the tool.
        </p>
        <ErrorLine message={formError} />
        {formSuccess && <div className="pill pill-ok" style={{ marginBottom: "0.75rem" }}>{formSuccess}</div>}
        <form className="form-grid" onSubmit={handleSubmit}>
          <div className="field-row">
            <div>
              <label htmlFor="agent-name">Name</label>
              <input
                id="agent-name"
                type="text"
                value={form.name}
                onChange={(e) => updateField("name", e.target.value)}
                placeholder="researcher"
                required
              />
            </div>
            <div>
              <label htmlFor="agent-provider">Provider</label>
              <select
                id="agent-provider"
                value={form.provider}
                onChange={(e) => updateField("provider", e.target.value)}
              >
                <option value="gemini">gemini</option>
                <option value="huggingface">huggingface</option>
              </select>
            </div>
          </div>

          <div className="field-row">
            <div>
              <label htmlFor="agent-model">Model</label>
              <input
                id="agent-model"
                type="text"
                value={form.model}
                onChange={(e) => updateField("model", e.target.value)}
                placeholder="gemini-2.0-flash"
              />
            </div>
            <div>
              <label htmlFor="agent-key">API key</label>
              <input
                id="agent-key"
                type="password"
                value={form.key}
                onChange={(e) => updateField("key", e.target.value)}
                placeholder="stored encrypted"
              />
            </div>
          </div>

          <div>
            <label htmlFor="agent-prompt">Prompt</label>
            <textarea
              id="agent-prompt"
              value={form.prompt}
              onChange={(e) => updateField("prompt", e.target.value)}
              placeholder="System prompt / role instructions for this agent…"
            />
          </div>

          <div>
            <label htmlFor="agent-roles">Roles (comma-separated)</label>
            <input
              id="agent-roles"
              type="text"
              value={form.roles}
              onChange={(e) => updateField("roles", e.target.value)}
              placeholder="coder, reviewer"
            />
          </div>

          <details className="advanced">
            <summary>Advanced (mode, tools, guardrails)</summary>
            <div className="form-grid">
              <div className="field-row">
                <div>
                  <label htmlFor="agent-mode">Mode</label>
                  <select id="agent-mode" value={form.mode} onChange={(e) => updateField("mode", e.target.value)}>
                    <option value="auto">auto</option>
                    <option value="approval">approval</option>
                  </select>
                </div>
                <div>
                  <label htmlFor="agent-maxcost">Max cost</label>
                  <input
                    id="agent-maxcost"
                    type="number"
                    step="0.01"
                    min="0"
                    value={form.maxCost}
                    onChange={(e) => updateField("maxCost", e.target.value)}
                  />
                </div>
              </div>
              <div className="field-row">
                <div>
                  <label htmlFor="agent-ratelimit">Rate limit</label>
                  <input
                    id="agent-ratelimit"
                    type="number"
                    min="0"
                    value={form.rateLimit}
                    onChange={(e) => updateField("rateLimit", e.target.value)}
                  />
                </div>
                <div>
                  <label htmlFor="agent-tools">Tools (comma-separated)</label>
                  <input
                    id="agent-tools"
                    type="text"
                    value={form.tools}
                    onChange={(e) => updateField("tools", e.target.value)}
                  />
                </div>
              </div>
              <div>
                <label htmlFor="agent-blocked">Blocked tools (comma-separated)</label>
                <input
                  id="agent-blocked"
                  type="text"
                  value={form.blockedTools}
                  onChange={(e) => updateField("blockedTools", e.target.value)}
                />
              </div>
            </div>
          </details>

          <div>
            <button className="btn btn-primary" type="submit" disabled={submitting}>
              {submitting ? "Creating…" : "Create agent"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
