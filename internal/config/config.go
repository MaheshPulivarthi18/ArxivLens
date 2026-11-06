package config

import (
	"log"
	"os"
	"strings"
	"time"
)

// DefaultIngestTimeout bounds the synchronous chunk+embed call that on-demand ingest makes to the
// embedder's /internal/ingest. On a CPU-only host a ~60-chunk paper takes ~50 s, so 45 s was too short.
const DefaultIngestTimeout = 5 * time.Minute

type Config struct {
	PostgresURL    string
	KafkaBrokers   []string
	MinioEndpoint  string
	MinioAccessKey string
	MinioSecretKey string
	MinioBucket    string
	MinioUseSSL    bool
	IngestTimeout  time.Duration
}

func LoadConfig() *Config {
	pgURL := getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/arxivlens?sslmode=disable")
	brokersStr := getEnv("KAFKA_BROKERS", "localhost:29092")
	brokers := strings.Split(brokersStr, ",")

	minioEndpoint := getEnv("MINIO_ENDPOINT", "localhost:9000")
	minioAccessKey := getEnv("MINIO_ACCESS_KEY", "minioadmin")
	minioSecretKey := getEnv("MINIO_SECRET_KEY", "minioadmin")
	minioBucket := getEnv("MINIO_BUCKET", "arxiv-papers")
	minioUseSSL := getEnv("MINIO_USE_SSL", "false") == "true"

	return &Config{
		PostgresURL:    pgURL,
		KafkaBrokers:   brokers,
		MinioEndpoint:  minioEndpoint,
		MinioAccessKey: minioAccessKey,
		MinioSecretKey: minioSecretKey,
		MinioBucket:    minioBucket,
		MinioUseSSL:    minioUseSSL,
		IngestTimeout:  parseDuration("INGEST_TIMEOUT", os.Getenv("INGEST_TIMEOUT"), DefaultIngestTimeout),
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

// parseDuration parses a Go duration such as "5m" or "90s". Empty, invalid or non-positive values
// fall back to def (invalid ones are logged so a typo doesn't go unnoticed).
func parseDuration(key, val string, def time.Duration) time.Duration {
	if val == "" {
		return def
	}
	d, err := time.ParseDuration(val)
	if err != nil || d <= 0 {
		log.Printf("[Config] Ignoring %s=%q (want a positive Go duration such as 5m), using %s", key, val, def)
		return def
	}
	return d
}
