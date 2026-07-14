package storage

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/WindAster/server/internals/config"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// minioStorage implements Storage against a MinIO / S3-compatible backend.
//
// Two clients are kept: `internal` talks to MinIO from the server (Stat/Delete/
// bucket setup), while `public` is used only to *sign* browser-facing URLs so
// the host in the signature matches what the browser actually connects to. The
// public client never makes network calls here (presigning is a local HMAC).
type minioStorage struct {
	internal *minio.Client
	public   *minio.Client
	bucket   string
	expiry   time.Duration
}

// NewMinio builds a MinIO-backed Storage and ensures the bucket exists.
func NewMinio(cfg config.Config) (Storage, error) {
	intHost, intSecure := parseEndpoint(cfg.MinioEndpoint, cfg.MinioUseSSL)
	pubHost, pubSecure := parseEndpoint(cfg.MinioPublicEndpoint, cfg.MinioUseSSL)

	creds := credentials.NewStaticV4(cfg.MinioAccessKey, cfg.MinioSecretKey, "")

	internal, err := minio.New(intHost, &minio.Options{Creds: creds, Secure: intSecure})
	if err != nil {
		return nil, fmt.Errorf("minio internal client: %w", err)
	}
	public, err := minio.New(pubHost, &minio.Options{Creds: creds, Secure: pubSecure})
	if err != nil {
		return nil, fmt.Errorf("minio public client: %w", err)
	}

	s := &minioStorage{
		internal: internal,
		public:   public,
		bucket:   cfg.MinioBucket,
		expiry:   cfg.UploadURLExpiry,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	exists, err := internal.BucketExists(ctx, s.bucket)
	if err != nil {
		return nil, fmt.Errorf("minio bucket check: %w", err)
	}
	if !exists {
		if err := internal.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("minio make bucket: %w", err)
		}
	}
	return s, nil
}

func (s *minioStorage) PresignPut(ctx context.Context, key string) (string, error) {
	u, err := s.public.PresignedPutObject(ctx, s.bucket, key, s.expiry)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *minioStorage) PresignGet(ctx context.Context, key, downloadName string, inline bool) (string, error) {
	reqParams := make(url.Values)
	if inline {
		reqParams.Set("response-content-disposition", "inline")
	} else {
		reqParams.Set("response-content-disposition",
			fmt.Sprintf("attachment; filename=%q", downloadName))
	}
	u, err := s.public.PresignedGetObject(ctx, s.bucket, key, s.expiry, reqParams)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (s *minioStorage) Stat(ctx context.Context, key string) (int64, string, error) {
	info, err := s.internal.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return 0, "", err
	}
	return info.Size, info.ContentType, nil
}

func (s *minioStorage) Delete(ctx context.Context, key string) error {
	return s.internal.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
}

// parseEndpoint splits an optional scheme off an endpoint string, returning the
// bare host:port and whether TLS should be used. If no scheme is present the
// provided default is used.
func parseEndpoint(ep string, defaultSSL bool) (host string, secure bool) {
	switch {
	case strings.HasPrefix(ep, "https://"):
		return strings.TrimPrefix(ep, "https://"), true
	case strings.HasPrefix(ep, "http://"):
		return strings.TrimPrefix(ep, "http://"), false
	default:
		return ep, defaultSSL
	}
}
