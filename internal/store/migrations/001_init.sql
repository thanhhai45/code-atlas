-- PostgreSQL is the source of truth. Elasticsearch is a derived, rebuildable index.
CREATE TABLE IF NOT EXISTS repositories (
    github_id       BIGINT PRIMARY KEY,
    name            TEXT        NOT NULL,
    full_name       TEXT        NOT NULL,
    owner           TEXT        NOT NULL,
    description     TEXT        NOT NULL DEFAULT '',
    url             TEXT        NOT NULL,
    homepage        TEXT        NOT NULL DEFAULT '',
    language        TEXT        NOT NULL DEFAULT '',
    topics          TEXT[]      NOT NULL DEFAULT '{}',
    license         TEXT        NOT NULL DEFAULT '',
    license_name    TEXT        NOT NULL DEFAULT '',
    stars           INTEGER     NOT NULL DEFAULT 0,
    forks           INTEGER     NOT NULL DEFAULT 0,
    watchers        INTEGER     NOT NULL DEFAULT 0,
    open_issues     INTEGER     NOT NULL DEFAULT 0,
    default_branch  TEXT        NOT NULL DEFAULT '',
    archived        BOOLEAN     NOT NULL DEFAULT FALSE,
    fork            BOOLEAN     NOT NULL DEFAULT FALSE,
    readme          TEXT        NOT NULL DEFAULT '',
    categories      TEXT[]      NOT NULL DEFAULT '{}',
    technologies    TEXT[]      NOT NULL DEFAULT '{}',
    use_cases       TEXT[]      NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    pushed_at       TIMESTAMPTZ NOT NULL,
    synced_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS repositories_full_name_idx ON repositories (lower(full_name));
CREATE INDEX IF NOT EXISTS repositories_stars_idx ON repositories (stars DESC);
CREATE INDEX IF NOT EXISTS repositories_synced_at_idx ON repositories (synced_at);

-- One row per crawler invocation: the basis for incremental sync and failure tracking.
CREATE TABLE IF NOT EXISTS crawl_runs (
    id           BIGSERIAL PRIMARY KEY,
    query        TEXT        NOT NULL,
    status       TEXT        NOT NULL DEFAULT 'running',
    fetched      INTEGER     NOT NULL DEFAULT 0,
    indexed      INTEGER     NOT NULL DEFAULT 0,
    failed       INTEGER     NOT NULL DEFAULT 0,
    error        TEXT        NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ
);
