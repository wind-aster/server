package storage

import (
	"context"
	"time"
)

// Storage abstracts object storage so the backend is not coupled to a specific
// provider. The current implementation targets self-hosted MinIO via the S3
// API (see minio.go), but any S3-compatible / object store can be dropped in.
type Storage interface {
	// PresignPut returns a short-lived URL the browser can PUT an object to
	// directly, bypassing the app server.
	PresignPut(ctx context.Context, key string) (string, error)

	// PresignGet returns a presigned URL (valid for expiry) to fetch an object.
	// When inline is true the object is served for inline display (images);
	// otherwise it is served as a download with the given filename. The signed
	// URL also carries an immutable Cache-Control so browsers can cache the bytes.
	PresignGet(ctx context.Context, key, downloadName string, inline bool, expiry time.Duration) (string, error)

	// Stat returns the object's size and content type, or an error if it does
	// not exist. Used to confirm a direct upload actually completed.
	Stat(ctx context.Context, key string) (size int64, contentType string, err error)

	// Delete removes an object. Best-effort; used to clean up rejected uploads.
	Delete(ctx context.Context, key string) error
}
