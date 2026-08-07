CREATE TABLE IF NOT EXISTS tiles (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    title       TEXT NOT NULL,
    url         TEXT NOT NULL,
    icon        TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    position    INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS notes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    body       TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS todos (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    text       TEXT NOT NULL,
    done       INTEGER NOT NULL DEFAULT 0,
    position   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS events (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    title            TEXT NOT NULL,
    notes            TEXT NOT NULL DEFAULT '',
    location         TEXT NOT NULL DEFAULT '',
    attendees        TEXT NOT NULL DEFAULT '',
    starts_at        TEXT NOT NULL,
    ends_at          TEXT NOT NULL,
    all_day          INTEGER NOT NULL DEFAULT 0,
    color            TEXT NOT NULL DEFAULT '#5b9dff',
    recurrence       TEXT NOT NULL DEFAULT 'none',
    recurrence_until TEXT NOT NULL DEFAULT '',
    countdown        INTEGER NOT NULL DEFAULT 0,
    important        INTEGER NOT NULL DEFAULT 0,
    reminded         INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at       TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_events_starts_at ON events (starts_at);

-- Per-Telegram-user bot preferences. digest is the daily-digest opt-in (off by
-- default; each user turns it on with /digest on).
CREATE TABLE IF NOT EXISTS bot_prefs (
    chat_id    INTEGER PRIMARY KEY,
    digest     INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Weather locations a user has saved (each user can keep several).
CREATE TABLE IF NOT EXISTS user_locations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id    INTEGER NOT NULL,
    label      TEXT NOT NULL,
    lat        REAL NOT NULL,
    lon        REAL NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_user_locations_chat ON user_locations (chat_id);

-- Per-user project tracker (owner_id = Telegram chat id for now). Headline
-- figures are rolled up from the latest update.
CREATE TABLE IF NOT EXISTS projects (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_id         INTEGER NOT NULL,
    name             TEXT NOT NULL,
    description      TEXT NOT NULL DEFAULT '',
    percent_complete REAL NOT NULL DEFAULT 0,
    est_eng_hours    REAL NOT NULL DEFAULT 0,
    est_agent_hours  REAL NOT NULL DEFAULT 0,
    bugs             INTEGER NOT NULL DEFAULT 0,
    first_pass       INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at       TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_projects_owner ON projects (owner_id);

-- Point-in-time updates under a project.
CREATE TABLE IF NOT EXISTS project_updates (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id       INTEGER NOT NULL,
    percent_complete REAL NOT NULL DEFAULT 0,
    est_eng_hours    REAL NOT NULL DEFAULT 0,
    est_agent_hours  REAL NOT NULL DEFAULT 0,
    eta              TEXT NOT NULL DEFAULT '',
    notes            TEXT NOT NULL DEFAULT '',
    bugs_found       INTEGER NOT NULL DEFAULT 0,
    bugs_fixed       INTEGER NOT NULL DEFAULT 0,
    created_at       TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_project_updates_project ON project_updates (project_id);

-- Per-user personal self check-ins.
CREATE TABLE IF NOT EXISTS checkins (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_id   INTEGER NOT NULL,
    mood       INTEGER NOT NULL DEFAULT 0,
    energy     INTEGER NOT NULL DEFAULT 0,
    focus      INTEGER NOT NULL DEFAULT 0,
    note       TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_checkins_owner ON checkins (owner_id);

-- Per-user goals (Telegram-only for now; surfaced in the digest). target_date is
-- an optional RFC3339 date the goal is aimed at.
CREATE TABLE IF NOT EXISTS goals (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_id    INTEGER NOT NULL,
    title       TEXT NOT NULL,
    detail      TEXT NOT NULL DEFAULT '',
    target_date TEXT NOT NULL DEFAULT '',
    done        INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_goals_owner ON goals (owner_id);
