-- Attachments for chat messages.
-- No migration tool is wired up (createTables() in internals/database is
-- disabled); apply manually:  psql "$DB_DSN" -f migrations/0001_create_attachments.sql
CREATE TABLE IF NOT EXISTS attachments (
    id          SERIAL PRIMARY KEY,
    message_id  INTEGER REFERENCES messages(id) ON DELETE CASCADE, -- NULL until the message is sent
    uploader_id INTEGER NOT NULL REFERENCES users(id),
    chat_id     INTEGER NOT NULL REFERENCES chats(id),
    storage_key TEXT    NOT NULL,
    thumb_key   TEXT,
    filename    TEXT    NOT NULL,
    mime_type   TEXT    NOT NULL,
    size_bytes  BIGINT  NOT NULL DEFAULT 0,
    width       INTEGER,
    height      INTEGER,
    status      TEXT    NOT NULL DEFAULT 'pending', -- 'pending' | 'ready'
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_attachments_message ON attachments (message_id);
CREATE INDEX IF NOT EXISTS idx_attachments_chat ON attachments (chat_id);
