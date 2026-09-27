-- Named engine connections (#44): snapshots and template_db_built
-- must route GC and cache-hit probes at the SERVER a template lives
-- on, so both carry the `connection` selector (empty = the singular
-- connections.<family> block).
--
-- snapshots gets a plain additive column (rows predate named blocks,
-- so '' is correct for all of them).
--
-- template_db_built's cache-hit gate keys on (worktree, db_key,
-- engine); two entries on one family can now target different
-- servers, and the same db_key must track its build per connection —
-- so the key grows a `connection` column. SQLite can't alter a PK:
-- rebuild the table and copy the rows across with connection=''.
ALTER TABLE snapshots ADD COLUMN connection TEXT NOT NULL DEFAULT '';

CREATE TABLE template_db_built_new (
    worktree_id INTEGER NOT NULL REFERENCES worktrees(id) ON DELETE CASCADE,
    db_key      TEXT    NOT NULL,
    engine      TEXT    NOT NULL,
    connection  TEXT    NOT NULL DEFAULT '',
    fingerprint TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (worktree_id, db_key, engine, connection)
);

INSERT INTO template_db_built_new (
    worktree_id, db_key, engine, fingerprint, updated_at, connection
)
SELECT worktree_id,
    db_key,
    engine,
    fingerprint,
    updated_at,
    ''
FROM template_db_built;

DROP TABLE template_db_built;
ALTER TABLE template_db_built_new RENAME TO template_db_built;
