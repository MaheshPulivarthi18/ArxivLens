package config

import (
	"os"
	"strings"
)

type Config struct {
	PostgresURL     string
	KafkaBrokers    []string
	MinioEndpoint   string
	MinioAccessKey  string
	MinioSecretKey  string
	MinioBucket     string
	MinioUseSSL     bool
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
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
