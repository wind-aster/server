package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all runtime configuration loaded from the environment.
type Config struct {
	DBDSN string

	MinioEndpoint       string // server -> MinIO (internal), host:port or scheme://host:port
	MinioPublicEndpoint string // host used when signing browser-facing URLs
	MinioAccessKey      string
	MinioSecretKey      string
	MinioBucket         string
	MinioUseSSL         bool

	MaxUploadSize   int64         // bytes
	UploadURLExpiry time.Duration // presigned URL lifetime
}

// Load reads configuration from environment variables (populated via godotenv
// in main). Sensible defaults are applied for local development.
func Load() Config {
	endpoint := getEnv("MINIO_ENDPOINT", "localhost:9000")
	return Config{
		DBDSN:               os.Getenv("DB_DSN"),
		MinioEndpoint:       endpoint,
		MinioPublicEndpoint: getEnv("MINIO_PUBLIC_ENDPOINT", endpoint),
		MinioAccessKey:      os.Getenv("MINIO_ACCESS_KEY"),
		MinioSecretKey:      os.Getenv("MINIO_SECRET_KEY"),
		MinioBucket:         getEnv("MINIO_BUCKET", "windaster-files"),
		MinioUseSSL:         getEnv("MINIO_USE_SSL", "false") == "true",
		MaxUploadSize:       getInt64("MAX_UPLOAD_SIZE", 26214400), // 25 MiB
		UploadURLExpiry:     time.Duration(getInt64("UPLOAD_URL_EXPIRY_SECONDS", 900)) * time.Second,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}
