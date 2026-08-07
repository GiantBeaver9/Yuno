// Thin fetch wrapper for the Yuno REST API. Every call resolves to parsed
// JSON on success or throws an Error with a human-readable message on
// failure (network error, non-2xx, or a {error: "..."} body).

async function request(path, options = {}) {
  let res;
  try {
    res = await fetch(path, {
      headers: { "Content-Type": "application/json", ...(options.headers || {}) },
      ...options,
    });
  } catch (err) {
    throw new Error(`Network error calling ${path}: ${err.message}`);
  }

  const text = await res.text();
  let data = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      // Non-JSON body (shouldn't happen for this API); leave data null.
    }
  }

  if (!res.ok) {
    const msg = (data && (data.error || data.message)) || `HTTP ${res.status} ${res.statusText}`;
    const err = new Error(msg);
    err.status = res.status;
    throw err;
  }

  return data;
}

export const api = {
  health: () => request("/api/health"),

  listAgents: () => request("/api/agents"),
  createAgent: (body) => request("/api/agents", { method: "POST", body: JSON.stringify(body) }),

  listWorkflows: () => request("/api/workflows"),
  getWorkflow: (id) => request(`/api/workflows/${id}`),

  listRuns: () => request("/api/runs"),
  createRun: (body) => request("/api/runs", { method: "POST", body: JSON.stringify(body) }),

  getRunMessages: (guid) => request(`/api/runs/${guid}/messages`),
  postRunMessage: (guid, body) =>
    request(`/api/runs/${guid}/messages`, { method: "POST", body: JSON.stringify(body) }),

  approveMessage: (id) => request(`/api/messages/${id}/approve`, { method: "POST" }),
  resumeRun: (guid) => request(`/api/runs/${guid}/resume`, { method: "POST" }),
};

// Splits a comma-separated field into a trimmed, non-empty string array.
export function splitList(value) {
  return value
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}
