package handlers

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/WindAster/server/internals/database"
)

// StartAttachmentCleanup periodically removes abandoned uploads: attachments
// still 'pending' (uploaded but never linked to a sent message) older than ttl,
// deleting both their object-storage objects and DB rows. Runs once immediately,
// then every interval. Best-effort — failures are logged, not fatal.
func StartAttachmentCleanup(interval, ttl time.Duration) {
	go func() {
		for {
			runAttachmentCleanup(ttl)
			time.Sleep(interval)
		}
	}()
}

func runAttachmentCleanup(ttl time.Duration) {
	if Store == nil {
		return
	}
	cutoff := time.Now().Add(-ttl)
	rows, err := database.DB.Query(
		`SELECT id, storage_key, thumb_key FROM attachments
		 WHERE status = 'pending' AND created_at < $1`, cutoff)
	if err != nil {
		log.Printf("cleanup: query failed: %v", err)
		return
	}

	type orphan struct {
		id         int
		storageKey string
		thumbKey   sql.NullString
	}
	var orphans []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.id, &o.storageKey, &o.thumbKey); err != nil {
			log.Printf("cleanup: scan failed: %v", err)
			continue
		}
		orphans = append(orphans, o)
	}
	rows.Close()

	ctx := context.Background()
	for _, o := range orphans {
		_ = Store.Delete(ctx, o.storageKey)
		if o.thumbKey.Valid && o.thumbKey.String != "" {
			_ = Store.Delete(ctx, o.thumbKey.String)
		}
		if _, err := database.DB.Exec(`DELETE FROM attachments WHERE id = $1`, o.id); err != nil {
			log.Printf("cleanup: delete row %d failed: %v", o.id, err)
		}
	}
	if len(orphans) > 0 {
		log.Printf("cleanup: removed %d abandoned attachment(s)", len(orphans))
	}
}
