-- Yuno schema (Postgres). One idempotent file; FKs indexed. Mirrors PRD §5.
-- Hierarchy: project -> build -> ticket -> run(guid) -> messages / stack / dict.

-- project = the PRD/intent. Durable, shared across attempts.
CREATE TABLE IF NOT EXISTS project (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL,
    prd        TEXT NOT NULL DEFAULT '',
    repo_url   TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- build = one attempt at a project. Surrogate PK id, human build_number.
CREATE TABLE IF NOT EXISTS build (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id   BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE,
    build_number INTEGER NOT NULL,
    branch_name  TEXT NOT NULL DEFAULT '',
    pr_number    INTEGER,
    label        TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'open',
    artifact_ref TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, build_number)
);
CREATE INDEX IF NOT EXISTS idx_build_project ON build (project_id);

-- ticket = a unit of work within a build. (project, build, ticket) is unique.
CREATE TABLE IF NOT EXISTS ticket (
    id       BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    build_id BIGINT NOT NULL REFERENCES build(id) ON DELETE CASCADE,
    title    TEXT NOT NULL,
    status   TEXT NOT NULL DEFAULT 'open'
);
CREATE INDEX IF NOT EXISTS idx_ticket_build ON ticket (build_id);

-- agent = a configurable actor. Config knobs (§10/§11/§15) live here; the
-- encrypted provider key lives in provider_key.
CREATE TABLE IF NOT EXISTS agent (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          TEXT NOT NULL,
    provider      TEXT NOT NULL DEFAULT '',
    model         TEXT NOT NULL DEFAULT '',
    recipe_path   TEXT NOT NULL DEFAULT '',
    prompt        TEXT NOT NULL DEFAULT '',
    tools         TEXT[] NOT NULL DEFAULT '{}',
    mode          TEXT NOT NULL DEFAULT 'auto',      -- 'auto' | 'approval'
    max_cost      DOUBLE PRECISION NOT NULL DEFAULT 0,
    rate_limit    INTEGER NOT NULL DEFAULT 0,
    blocked_tools TEXT[] NOT NULL DEFAULT '{}',
    guid          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- role via join; mutable, non-unique (ADR-9).
CREATE TABLE IF NOT EXISTS agent_roles (
    agent_id BIGINT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    role     TEXT NOT NULL,
    PRIMARY KEY (agent_id, role)
);

-- provider_key = per-agent BYO key, encrypted at rest.
CREATE TABLE IF NOT EXISTS provider_key (
    agent_id BIGINT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    enc_key  BYTEA NOT NULL,
    PRIMARY KEY (agent_id, provider)
);

-- secret_github = fine-grained PAT, encrypted, per project.
CREATE TABLE IF NOT EXISTS secret_github (
    project_id BIGINT NOT NULL REFERENCES project(id) ON DELETE CASCADE PRIMARY KEY,
    enc_pat    BYTEA NOT NULL
);

-- workflow = nodes + edges as data (ADR-15).
CREATE TABLE IF NOT EXISTS workflow (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        TEXT NOT NULL,
    is_template BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS node (
    workflow_id BIGINT NOT NULL REFERENCES workflow(id) ON DELETE CASCADE,
    node_key    TEXT NOT NULL,
    agent_id    BIGINT NOT NULL REFERENCES agent(id),
    is_entry    BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (workflow_id, node_key)
);

CREATE TABLE IF NOT EXISTS edge (
    workflow_id BIGINT NOT NULL REFERENCES workflow(id) ON DELETE CASCADE,
    from_node   TEXT NOT NULL,
    to_node     TEXT NOT NULL,
    on_decision TEXT NOT NULL,   -- 'approve' | 'reject' | 'complete'
    PRIMARY KEY (workflow_id, from_node, on_decision)
);

-- run = a workflow execution; guid is the spine.
CREATE TABLE IF NOT EXISTS run (
    guid           TEXT PRIMARY KEY,
    ticket_id      BIGINT REFERENCES ticket(id) ON DELETE SET NULL,
    workflow_id    BIGINT REFERENCES workflow(id),
    status         TEXT NOT NULL DEFAULT 'running',
    max_iterations INTEGER NOT NULL DEFAULT 10,
    iterations     INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- message = the bus + trail. id is the global monotonic SSE cursor; seq is the
-- per-run replay cursor.
CREATE TABLE IF NOT EXISTS message (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id      TEXT NOT NULL REFERENCES run(guid) ON DELETE CASCADE,
    seq         BIGINT NOT NULL,
    from_ref    TEXT NOT NULL,
    to_ref      TEXT NOT NULL,
    content     TEXT NOT NULL DEFAULT '',
    decision    TEXT NOT NULL DEFAULT '',
    summary     TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'queued',  -- queued|processing|done|pending_approval|needs_human
    lease_until TIMESTAMPTZ,
    tokens      INTEGER NOT NULL DEFAULT 0,
    cost        DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_message_run_seq ON message (run_id, seq);
CREATE INDEX IF NOT EXISTS idx_message_queue ON message (status, lease_until);

-- stack = ordered (summary, ticket_id) breadcrumbs, tagged with run guid.
CREATE TABLE IF NOT EXISTS stack (
    run_id    TEXT NOT NULL REFERENCES run(guid) ON DELETE CASCADE,
    seq       BIGINT NOT NULL,
    summary   TEXT NOT NULL DEFAULT '',
    ticket_id BIGINT REFERENCES ticket(id) ON DELETE SET NULL,
    PRIMARY KEY (run_id, seq)
);

-- dict = guid -> full_detail heavy payload, hydrated on demand.
CREATE TABLE IF NOT EXISTS dict (
    guid        TEXT PRIMARY KEY,
    full_detail JSONB NOT NULL DEFAULT '{}'::jsonb
);

-- schedule = a third message source (§16).
CREATE TABLE IF NOT EXISTS schedule (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id    BIGINT NOT NULL REFERENCES agent(id) ON DELETE CASCADE,
    cron_expr   TEXT NOT NULL DEFAULT '',
    next_run_at TIMESTAMPTZ,
    payload     TEXT NOT NULL DEFAULT '',
    enabled     BOOLEAN NOT NULL DEFAULT true
);
CREATE INDEX IF NOT EXISTS idx_schedule_due ON schedule (enabled, next_run_at);
