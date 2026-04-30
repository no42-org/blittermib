-- blittermib schema. Idempotent: applied at every startup.
--
-- Connection-level PRAGMAs (foreign_keys, journal_mode, busy_timeout,
-- synchronous) are intentionally NOT set here — they're per-connection
-- in SQLite, so they're applied via the sql.DB pool configuration in
-- store.go (currently MaxOpenConns(1)) so every query sees consistent
-- enforcement.

CREATE TABLE IF NOT EXISTS module (
    name           TEXT    PRIMARY KEY,
    oid_root       TEXT    NOT NULL DEFAULT '',
    organization   TEXT    NOT NULL DEFAULT '',
    contact_info   TEXT    NOT NULL DEFAULT '',
    description    TEXT    NOT NULL DEFAULT '',
    last_updated   TEXT    NOT NULL DEFAULT '',
    source_path    TEXT    NOT NULL DEFAULT '',
    parse_status   TEXT    NOT NULL DEFAULT 'clean'
);

CREATE TABLE IF NOT EXISTS symbol (
    id             INTEGER PRIMARY KEY,
    module_name    TEXT    NOT NULL REFERENCES module(name) ON DELETE CASCADE,
    name           TEXT    NOT NULL,
    oid            TEXT    NOT NULL DEFAULT '',
    parent_oid     TEXT    NOT NULL DEFAULT '',
    kind           TEXT    NOT NULL,
    syntax         TEXT    NOT NULL DEFAULT '',
    access         TEXT    NOT NULL DEFAULT '',
    status         TEXT    NOT NULL DEFAULT '',
    units          TEXT    NOT NULL DEFAULT '',
    reference_text TEXT    NOT NULL DEFAULT '',
    description    TEXT    NOT NULL DEFAULT '',
    default_value  TEXT    NOT NULL DEFAULT '',
    is_table       INTEGER NOT NULL DEFAULT 0,
    is_table_entry INTEGER NOT NULL DEFAULT 0,
    augments       TEXT    NOT NULL DEFAULT '',
    index_columns  TEXT    NOT NULL DEFAULT '',  -- JSON array
    source_line    INTEGER NOT NULL DEFAULT 0,
    UNIQUE (module_name, name)
);

CREATE INDEX IF NOT EXISTS symbol_oid_idx        ON symbol(oid);
CREATE INDEX IF NOT EXISTS symbol_parent_oid_idx ON symbol(parent_oid);
CREATE INDEX IF NOT EXISTS symbol_kind_idx       ON symbol(kind);
CREATE INDEX IF NOT EXISTS symbol_status_idx     ON symbol(status);
-- Powers store.LookupByName, which is hit on every /s/{name}
-- disambiguation request. Without this, every bare-name lookup
-- is a full table scan over `symbol`.
CREATE INDEX IF NOT EXISTS symbol_name_idx       ON symbol(name);

-- Cross-references are keyed by qualified Module::Name pair, not row id.
-- This makes hot-reload trivial: dropping a module's outgoing refs is a
-- single DELETE; refs INTO the module from other modules stay valid
-- because they were never tied to the module's row ids.
CREATE TABLE IF NOT EXISTS reference (
    source_module TEXT NOT NULL,
    source_name   TEXT NOT NULL,
    target_module TEXT NOT NULL,
    target_name   TEXT NOT NULL,
    kind          TEXT NOT NULL,
    PRIMARY KEY (source_module, source_name, target_module, target_name, kind)
);

CREATE INDEX IF NOT EXISTS reference_target_idx ON reference(target_module, target_name);

CREATE TABLE IF NOT EXISTS diagnostic (
    id          INTEGER PRIMARY KEY,
    module_name TEXT    NOT NULL DEFAULT '',
    file        TEXT    NOT NULL DEFAULT '',
    line        INTEGER NOT NULL DEFAULT 0,
    severity    TEXT    NOT NULL,
    code        TEXT    NOT NULL DEFAULT '',
    message     TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS diagnostic_module_idx   ON diagnostic(module_name);
CREATE INDEX IF NOT EXISTS diagnostic_severity_idx ON diagnostic(severity);

-- FTS5 full-text index over the searchable fields of `symbol`.
-- content='symbol' makes this an external-content index;
-- content_rowid='id' joins back to the source row.
CREATE VIRTUAL TABLE IF NOT EXISTS symbol_fts USING fts5(
    name, oid, description, module_name,
    content='symbol',
    content_rowid='id',
    tokenize='porter unicode61 remove_diacritics 1'
);

-- Triggers keep the FTS index synchronised with `symbol` writes.
CREATE TRIGGER IF NOT EXISTS symbol_ai AFTER INSERT ON symbol BEGIN
    INSERT INTO symbol_fts(rowid, name, oid, description, module_name)
    VALUES (new.id, new.name, new.oid, new.description, new.module_name);
END;

CREATE TRIGGER IF NOT EXISTS symbol_ad AFTER DELETE ON symbol BEGIN
    INSERT INTO symbol_fts(symbol_fts, rowid, name, oid, description, module_name)
    VALUES ('delete', old.id, old.name, old.oid, old.description, old.module_name);
END;

CREATE TRIGGER IF NOT EXISTS symbol_au AFTER UPDATE ON symbol BEGIN
    INSERT INTO symbol_fts(symbol_fts, rowid, name, oid, description, module_name)
    VALUES ('delete', old.id, old.name, old.oid, old.description, old.module_name);
    INSERT INTO symbol_fts(rowid, name, oid, description, module_name)
    VALUES (new.id, new.name, new.oid, new.description, new.module_name);
END;
