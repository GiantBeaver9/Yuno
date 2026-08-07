import React, { useEffect, useState } from "react";
import { api } from "../api.js";
import { ErrorLine, EmptyLine, Pill } from "./common.jsx";

export default function WorkflowsTab() {
  const [workflows, setWorkflows] = useState([]);
  const [listError, setListError] = useState("");
  const [loading, setLoading] = useState(true);

  const [selectedId, setSelectedId] = useState(null);
  const [detail, setDetail] = useState(null);
  const [detailError, setDetailError] = useState("");
  const [detailLoading, setDetailLoading] = useState(false);

  useEffect(() => {
    api
      .listWorkflows()
      .then((data) => setWorkflows(data || []))
      .catch((err) => setListError(err.message))
      .finally(() => setLoading(false));
  }, []);

  async function select(id) {
    setSelectedId(id);
    setDetailLoading(true);
    setDetailError("");
    try {
      const data = await api.getWorkflow(id);
      setDetail(data);
    } catch (err) {
      setDetailError(err.message);
      setDetail(null);
    } finally {
      setDetailLoading(false);
    }
  }

  return (
    <div className="two-col">
      <div className="panel">
        <h2>Workflows</h2>
        <ErrorLine message={listError} />
        {loading ? (
          <EmptyLine>Loading workflows…</EmptyLine>
        ) : workflows.length === 0 ? (
          <EmptyLine>No workflows defined yet.</EmptyLine>
        ) : (
          <ul className="workflow-list">
            {workflows.map((w) => (
              <li
                key={w.id}
                className={w.id === selectedId ? "selected" : ""}
                onClick={() => select(w.id)}
              >
                <span>
                  <strong>{w.name}</strong>{" "}
                  <span className="muted" style={{ fontSize: "0.78rem" }}>
                    #{w.id}
                  </span>
                </span>
                {w.isTemplate && <Pill tone="accent">template</Pill>}
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="panel">
        <h2>DAG</h2>
        <ErrorLine message={detailError} />
        {!selectedId ? (
          <EmptyLine>Select a workflow to see its nodes and edges.</EmptyLine>
        ) : detailLoading ? (
          <EmptyLine>Loading…</EmptyLine>
        ) : detail ? (
          <WorkflowDetail detail={detail} />
        ) : null}
      </div>
    </div>
  );
}

function WorkflowDetail({ detail }) {
  const nodes = detail.nodes || [];
  const edges = detail.edges || [];
  return (
    <div className="stack">
      <div>
        <div className="hint" style={{ marginBottom: "0.4rem" }}>
          Nodes ({nodes.length})
        </div>
        <div style={{ display: "flex", flexWrap: "wrap", gap: "0.4rem" }}>
          {nodes.length === 0 && <span className="muted">no nodes</span>}
          {nodes.map((n) => (
            <span key={n.NodeKey} className={"node-chip" + (n.IsEntry ? " entry" : "")}>
              {n.NodeKey}
              {n.IsEntry && <Pill tone="accent">entry</Pill>}
              <span className="muted">agent #{n.AgentID}</span>
            </span>
          ))}
        </div>
      </div>

      <div>
        <div className="hint" style={{ marginBottom: "0.4rem" }}>
          Edges ({edges.length})
        </div>
        {edges.length === 0 ? (
          <span className="muted">no edges</span>
        ) : (
          <ul className="dag-list">
            {edges.map((e, i) => (
              <li key={i}>
                {e.FromNode}
                <span className="dag-arrow">--{e.OnDecision}--&gt;</span>
                {e.ToNode}
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
