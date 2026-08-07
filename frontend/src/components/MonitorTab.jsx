import React, { useEffect, useRef, useState } from "react";
import { EmptyLine, Pill, statusTone, timeNow } from "./common.jsx";

const MAX_EVENTS = 300;

export default function MonitorTab() {
  const [events, setEvents] = useState([]);
  const [connection, setConnection] = useState("connecting"); // connecting | open | error
  const [paused, setPaused] = useState(false);
  const seenIds = useRef(new Set());

  useEffect(() => {
    let source;
    try {
      source = new EventSource("/api/stream");
    } catch (err) {
      setConnection("error");
      return;
    }

    source.onopen = () => setConnection("open");
    source.onerror = () => setConnection("error");

    source.onmessage = (evt) => {
      let row;
      try {
        row = JSON.parse(evt.data);
      } catch {
        return;
      }
      setEvents((prev) => {
        const key = row.ID ?? `${row.RunID}-${row.Seq}-${prev.length}`;
        if (seenIds.current.has(key)) return prev;
        seenIds.current.add(key);
        const withMeta = { ...row, _key: key, _receivedAt: timeNow() };
        const next = [withMeta, ...prev];
        return next.length > MAX_EVENTS ? next.slice(0, MAX_EVENTS) : next;
      });
    };

    return () => source.close();
  }, []);

  const visibleEvents = paused ? [] : events;

  return (
    <div className="panel">
      <div className="section-row">
        <h2>Live monitor</h2>
        <div className="monitor-status">
          <ConnectionBadge state={connection} />
          <button className="btn btn-sm" onClick={() => setPaused((p) => !p)}>
            {paused ? "Resume feed" : "Pause"}
          </button>
          <button
            className="btn btn-sm"
            onClick={() => {
              setEvents([]);
              seenIds.current = new Set();
            }}
          >
            Clear
          </button>
        </div>
      </div>
      <p className="caption">
        Tailing <code>GET /api/stream</code> (Server-Sent Events) — every message row lands here the moment
        it hits the bus, newest first. This is the raw observability feed behind the run message trail.
      </p>

      {paused ? (
        <EmptyLine>Feed paused ({events.length} buffered). Resume to keep watching.</EmptyLine>
      ) : events.length === 0 ? (
        <EmptyLine>
          {connection === "error" ? "Stream unavailable." : "Waiting for activity on the bus…"}
        </EmptyLine>
      ) : (
        <div className="feed">
          {visibleEvents.map((e, i) => (
            <div className="feed-item new" key={e._key} style={{ animationDelay: i === 0 ? "0s" : "" }}>
              <div className="feed-head">
                <span className="muted">{e._receivedAt}</span>
                <span className="feed-refs">
                  {e.FromRef} → {e.ToRef}
                </span>
                {e.Decision && <Pill tone={statusTone(e.Decision)}>{e.Decision}</Pill>}
                {e.Status && <Pill tone={statusTone(e.Status)}>{e.Status}</Pill>}
                {typeof e.RunID !== "undefined" && (
                  <span className="muted">run {String(e.RunID).slice(0, 8)}…</span>
                )}
              </div>
              {e.Content && <div className="feed-content">{e.Content}</div>}
              {e.Summary && <div className="feed-summary">{e.Summary}</div>}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function ConnectionBadge({ state }) {
  const label = { connecting: "connecting…", open: "live", error: "reconnecting…" }[state] || state;
  const dot = { connecting: "pending", open: "ok", error: "bad" }[state] || "";
  return (
    <span className="health-badge">
      <span className={`dot ${dot}`} />
      {label}
    </span>
  );
}
