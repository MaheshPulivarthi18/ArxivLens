package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
}

func Connect(ctx context.Context, connString string) (*DB, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse postgres config: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 2
	config.MaxConnLifetime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to postgres: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("postgres ping failed: %w", err)
	}

	return &DB{Pool: pool}, nil
}

func (d *DB) Close() {
	if d.Pool != nil {
		d.Pool.Close()
	}
}

// MigrationFiles returns the paths of every regular "*.up.sql" file directly
// inside dir, sorted lexically by file name (000001_... before 000002_...).
// Subdirectories and other files (e.g. "*.down.sql") are ignored. It returns
// an error if dir cannot be read or holds no up migrations.
func MigrationFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations dir %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no *.up.sql migrations found in %s", dir)
	}
	sort.Strings(names)
	paths := make([]string, len(names))
	for i, n := range names {
		paths[i] = filepath.Join(dir, n)
	}
	return paths, nil
}

func (d *DB) RunMigrations(ctx context.Context, migrationPath string) error {
	sqlBytes, err := os.ReadFile(migrationPath)
	if err != nil {
		return fmt.Errorf("failed to read migration file %s: %w", migrationPath, err)
	}

	_, err = d.Pool.Exec(ctx, string(sqlBytes))
	if err != nil {
		return fmt.Errorf("failed to execute migration: %w", err)
	}
	return nil
}

type PaperRecord struct {
	ID            int64
	ArxivID       string
	Version       int
	Title         string
	Abstract      string
	Authors       []string
	Categories    []string
	PublishedAt   time.Time
	SourceURL     string
	PDFStorageKey string
	SHA256        string
	Status        string
	StatusError   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// UpsertDiscoveredPaper performs natural-key upsert (Layer 1 idempotency)
func (d *DB) UpsertDiscoveredPaper(ctx context.Context, p *PaperRecord) (int64, error) {
	query := `
		INSERT INTO papers (
			arxiv_id, version, title, abstract, authors, categories,
			published_at, source_url, status, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'discovered', now())
		ON CONFLICT (arxiv_id, version) DO UPDATE SET
			title = EXCLUDED.title,
			abstract = EXCLUDED.abstract,
			authors = EXCLUDED.authors,
			categories = EXCLUDED.categories,
			published_at = EXCLUDED.published_at,
			source_url = EXCLUDED.source_url,
			updated_at = now()
		RETURNING id;
	`
	var id int64
	err := d.Pool.QueryRow(ctx, query,
		p.ArxivID, p.Version, p.Title, p.Abstract, p.Authors, p.Categories,
		p.PublishedAt, p.SourceURL,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("failed to upsert discovered paper: %w", err)
	}
	return id, nil
}

// UpdatePaperDownloaded updates paper record after download to MinIO
func (d *DB) UpdatePaperDownloaded(ctx context.Context, arxivID string, version int, storageKey, sha256 string) error {
	query := `
		UPDATE papers
		SET pdf_storage_key = $1,
		    sha256 = $2,
		    status = 'downloaded',
		    updated_at = now()
		WHERE arxiv_id = $3 AND version = $4;
	`
	tag, err := d.Pool.Exec(ctx, query, storageKey, sha256, arxivID, version)
	if err != nil {
		return fmt.Errorf("failed to update paper downloaded status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("paper not found for arxiv_id=%s version=%d", arxivID, version)
	}
	return nil
}

// RecordProcessedEvent checks and inserts event into the ledger (Layer 2 idempotency).
// Returns true if event is NEW (inserted), false if DUPLICATE (already processed).
func (d *DB) RecordProcessedEvent(ctx context.Context, tx pgx.Tx, consumerGroup, eventID string) (bool, error) {
	query := `
		INSERT INTO processed_events (consumer_group, event_id)
		VALUES ($1, $2)
		ON CONFLICT (consumer_group, event_id) DO NOTHING;
	`
	var tag pgx.Tx
	_ = tag
	cmdTag, err := tx.Exec(ctx, query, consumerGroup, eventID)
	if err != nil {
		return false, fmt.Errorf("failed to record processed event: %w", err)
	}
	return cmdTag.RowsAffected() > 0, nil
}

// GetPaperStatus returns current status of paper
func (d *DB) GetPaperStatus(ctx context.Context, arxivID string) (*PaperRecord, error) {
	query := `
		SELECT id, arxiv_id, version, title, abstract, authors, categories,
		       published_at, source_url, COALESCE(pdf_storage_key, ''), COALESCE(sha256, ''),
		       status, COALESCE(status_error, ''), created_at, updated_at
		FROM papers
		WHERE arxiv_id = $1
		ORDER BY version DESC
		LIMIT 1;
	`
	p := &PaperRecord{}
	var pubAt *time.Time
	err := d.Pool.QueryRow(ctx, query, arxivID).Scan(
		&p.ID, &p.ArxivID, &p.Version, &p.Title, &p.Abstract, &p.Authors, &p.Categories,
		&pubAt, &p.SourceURL, &p.PDFStorageKey, &p.SHA256,
		&p.Status, &p.StatusError, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if pubAt != nil {
		p.PublishedAt = *pubAt
	}
	return p, nil
}

type SystemStats struct {
	TotalPapers     int64          `json:"total_papers"`
	EmbeddedPapers  int64          `json:"embedded_papers"`
	TotalSections   int64          `json:"total_sections"`
	TotalChunks     int64          `json:"total_chunks"`
	TotalEmbeddings int64          `json:"total_embeddings"`
	Papers          []*PaperRecord `json:"papers"`
}

func (d *DB) GetSystemStats(ctx context.Context) (*SystemStats, error) {
	stats := &SystemStats{}

	_ = d.Pool.QueryRow(ctx, "SELECT count(*) FROM papers;").Scan(&stats.TotalPapers)
	_ = d.Pool.QueryRow(ctx, "SELECT count(*) FROM papers WHERE status = 'embedded';").Scan(&stats.EmbeddedPapers)
	_ = d.Pool.QueryRow(ctx, "SELECT count(*) FROM paper_sections;").Scan(&stats.TotalSections)
	_ = d.Pool.QueryRow(ctx, "SELECT count(*) FROM paper_chunks;").Scan(&stats.TotalChunks)
	_ = d.Pool.QueryRow(ctx, "SELECT count(*) FROM chunk_embeddings;").Scan(&stats.TotalEmbeddings)

	rows, err := d.Pool.Query(ctx, `
		SELECT id, arxiv_id, version, title, abstract, authors, categories,
		       published_at, source_url, COALESCE(pdf_storage_key, ''), COALESCE(sha256, ''),
		       status, COALESCE(status_error, ''), created_at, updated_at
		FROM papers
		ORDER BY id DESC
		LIMIT 100;
	`)
	if err != nil {
		return stats, nil
	}
	defer rows.Close()

	for rows.Next() {
		p := &PaperRecord{}
		var pubAt *time.Time
		if err := rows.Scan(
			&p.ID, &p.ArxivID, &p.Version, &p.Title, &p.Abstract, &p.Authors, &p.Categories,
			&pubAt, &p.SourceURL, &p.PDFStorageKey, &p.SHA256,
			&p.Status, &p.StatusError, &p.CreatedAt, &p.UpdatedAt,
		); err == nil {
			if pubAt != nil {
				p.PublishedAt = *pubAt
			}
			stats.Papers = append(stats.Papers, p)
		}
	}

	return stats, nil
}
