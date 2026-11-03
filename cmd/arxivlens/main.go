package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"arxivlens/internal/config"
	"arxivlens/internal/crawler"
	"arxivlens/internal/db"
	"arxivlens/internal/dlq"
	"arxivlens/internal/downloader"
	"arxivlens/internal/kafka"
	"arxivlens/internal/query"
	"arxivlens/internal/storage"
	"arxivlens/shared/events"

	"github.com/spf13/cobra"
)

var (
	cfg *config.Config
)

func main() {
	cfg = config.LoadConfig()

	rootCmd := &cobra.Command{
		Use:   "arxivlens",
		Short: "ArxivLens — Distributed Paper Ingestion & Semantic Search Pipeline",
	}

	rootCmd.AddCommand(newMigrateCmd())
	rootCmd.AddCommand(newCrawlCmd())
	rootCmd.AddCommand(newDownloadCmd())
	rootCmd.AddCommand(newStatusCmd())
	rootCmd.AddCommand(newQueryCmd())
	rootCmd.AddCommand(newDLQCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func newMigrateCmd() *cobra.Command {
	var (
		migrationDir  string
		migrationFile string
	)

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply database migrations to PostgreSQL",
		Long: "Applies every *.up.sql in --dir in lexical order. With --file, applies only that file.\n" +
			"Migrations must be idempotent (IF NOT EXISTS): there is no schema-version table.",
		RunE: func(cmd *cobra.Command, args []string) error {
			files := []string{migrationFile}
			if migrationFile == "" {
				var err error
				if files, err = db.MigrationFiles(migrationDir); err != nil {
					return err
				}
			}

			ctx := context.Background()
			database, err := db.Connect(ctx, cfg.PostgresURL)
			if err != nil {
				return err
			}
			defer database.Close()

			for _, f := range files {
				log.Printf("[Migration] Running migration file: %s", f)
				if err := database.RunMigrations(ctx, f); err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
			}

			log.Printf("[Migration] Database schema migrated successfully (%d file(s))", len(files))
			return nil
		},
	}

	cmd.Flags().StringVar(&migrationDir, "dir", "migrations", "Directory whose *.up.sql files are applied in lexical order")
	cmd.Flags().StringVar(&migrationFile, "file", "", "Apply only this migration SQL file (overrides --dir)")
	return cmd
}

func newCrawlCmd() *cobra.Command {
	var (
		query     string
		limit     int
		rateLimit time.Duration
	)

	cmd := &cobra.Command{
		Use:   "crawl",
		Short: "Crawl arXiv papers and publish to papers.discovered topic",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			database, err := db.Connect(ctx, cfg.PostgresURL)
			if err != nil {
				return err
			}
			defer database.Close()

			producer, err := kafka.NewProducer(cfg.KafkaBrokers)
			if err != nil {
				return err
			}
			defer producer.Close()

			c := crawler.NewCrawler(database, producer, rateLimit)
			count, err := c.Crawl(ctx, query, limit)
			if err != nil {
				return err
			}

			log.Printf("[Crawl] Successfully discovered and published %d papers", count)
			return nil
		},
	}

	cmd.Flags().StringVar(&query, "query", "cat:cs.AI OR cat:cs.LG", "arXiv search query")
	cmd.Flags().IntVar(&limit, "limit", 5, "Maximum number of papers to crawl")
	cmd.Flags().DurationVar(&rateLimit, "rate-limit", 3*time.Second, "Delay between arXiv requests")
	return cmd
}

func newDownloadCmd() *cobra.Command {
	var group string

	cmd := &cobra.Command{
		Use:   "download",
		Short: "Consume papers.discovered, download PDFs, upload to MinIO, and publish to papers.downloaded",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			database, err := db.Connect(ctx, cfg.PostgresURL)
			if err != nil {
				return err
			}
			defer database.Close()

			stor, err := storage.NewStorage(cfg.MinioEndpoint, cfg.MinioAccessKey, cfg.MinioSecretKey, cfg.MinioBucket, cfg.MinioUseSSL)
			if err != nil {
				return err
			}
			if err := stor.EnsureBucket(ctx); err != nil {
				return err
			}

			producer, err := kafka.NewProducer(cfg.KafkaBrokers)
			if err != nil {
				return err
			}
			defer producer.Close()

			dl := downloader.NewDownloader(database, stor, producer)

			consumer, err := kafka.NewConsumer(cfg.KafkaBrokers, group, []string{events.TopicPapersDiscovered}, producer)
			if err != nil {
				return err
			}
			defer consumer.Close()

			log.Printf("[Downloader] Worker started (consumer_group=%s)", group)
			return consumer.Start(ctx, dl.HandleRecord)
		},
	}

	cmd.Flags().StringVar(&group, "group", "downloader-v1", "Kafka consumer group ID")
	return cmd
}

func newStatusCmd() *cobra.Command {
	var arxivID string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Inspect paper status in PostgreSQL",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			database, err := db.Connect(ctx, cfg.PostgresURL)
			if err != nil {
				return err
			}
			defer database.Close()

			p, err := database.GetPaperStatus(ctx, arxivID)
			if err != nil {
				return fmt.Errorf("failed to get paper status: %w", err)
			}

			fmt.Println("==================================================")
			fmt.Printf("Paper ID:       %s (v%d)\n", p.ArxivID, p.Version)
			fmt.Printf("Title:          %s\n", p.Title)
			fmt.Printf("Status:         %s\n", p.Status)
			fmt.Printf("Categories:     %v\n", p.Categories)
			fmt.Printf("PDF Storage Key:%s\n", p.PDFStorageKey)
			fmt.Printf("SHA256:         %s\n", p.SHA256)
			fmt.Printf("Updated At:     %s\n", p.UpdatedAt.Format(time.RFC3339))
			fmt.Println("==================================================")
			return nil
		},
	}

	cmd.Flags().StringVar(&arxivID, "arxiv-id", "", "arXiv paper ID (required)")
	_ = cmd.MarkFlagRequired("arxiv-id")
	return cmd
}

func newQueryCmd() *cobra.Command {
	var (
		port        int
		embedderURL string
		modelName   string
	)

	cmd := &cobra.Command{
		Use:   "query",
		Short: "Start the Go RAG and Hybrid Query HTTP Service",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			database, err := db.Connect(ctx, cfg.PostgresURL)
			if err != nil {
				return err
			}
			defer database.Close()

			embedClient := query.NewEmbedClient(embedderURL)
			searchEngine := query.NewSearchEngine(database, embedClient, modelName)
			ragService := query.NewRAGService(searchEngine, embedClient)

			server := query.NewServer(database, searchEngine, ragService, port)

			minioStorage, err := storage.NewStorage(cfg.MinioEndpoint, cfg.MinioAccessKey, cfg.MinioSecretKey, cfg.MinioBucket, cfg.MinioUseSSL)
			if err == nil {
				ingester := query.NewPaperIngester(database, minioStorage.Client, cfg.MinioBucket, embedderURL)
				server.SetIngester(ingester)
			} else {
				log.Printf("[Query] Warning: MinIO storage init failed (%v), on-demand ingester disabled", err)
			}

			log.Printf("[Query] Starting Query Service on port %d (embedder: %s)...", port, embedderURL)
			return server.Start(ctx)
		},
	}

	cmd.Flags().IntVar(&port, "port", 8080, "HTTP server port")
	cmd.Flags().StringVar(&embedderURL, "embedder-url", "http://localhost:8081", "Embedder service base URL")
	cmd.Flags().StringVar(&modelName, "model", "BAAI/bge-base-en-v1.5", "Embedding model name")
	return cmd
}

func newDLQCmd() *cobra.Command {
	dlqCmd := &cobra.Command{
		Use:   "dlq",
		Short: "Manage and replay dead-letter queue (DLQ) messages",
	}

	var (
		topic     string
		key       string
		errorType string
		limit     int
		dryRun    bool
	)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List failed events in a DLQ topic",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			mgr := dlq.NewManager(cfg.KafkaBrokers)
			eventsList, err := mgr.ListEvents(ctx, topic, limit)
			if err != nil {
				return err
			}

			fmt.Printf("=== DLQ Topic: %s (Total: %d) ===\n", topic, len(eventsList))
			for i, ev := range eventsList {
				fmt.Printf("[%d] Key: %-15s | Error: %-18s | FailedAt: %s\n",
					i+1, ev.OriginalKey, ev.ErrorType, ev.FailedAt)
				fmt.Printf("    Message: %s\n", ev.ErrorMessage)
			}
			return nil
		},
	}
	listCmd.Flags().StringVar(&topic, "topic", "papers.downloaded.dlq", "DLQ topic name")
	listCmd.Flags().IntVar(&limit, "limit", 50, "Max messages to inspect")

	inspectCmd := &cobra.Command{
		Use:   "inspect",
		Short: "Inspect full error diagnostic context and stack trace for a paper key",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			mgr := dlq.NewManager(cfg.KafkaBrokers)
			eventsList, err := mgr.ListEvents(ctx, topic, 100)
			if err != nil {
				return err
			}

			found := false
			for _, ev := range eventsList {
				if ev.OriginalKey == key || key == "" {
					found = true
					fmt.Println("==================================================")
					fmt.Printf("Original Topic: %s (Partition %d, Offset %d)\n", ev.OriginalTopic, ev.Partition, ev.Offset)
					fmt.Printf("Paper Key:      %s\n", ev.OriginalKey)
					fmt.Printf("Error Type:     %s\n", ev.ErrorType)
					fmt.Printf("Error Message:  %s\n", ev.ErrorMessage)
					fmt.Printf("Attempts:       %d\n", ev.Attempts)
					fmt.Printf("Consumer Group: %s\n", ev.ConsumerGroup)
					fmt.Printf("Traceparent:    %s\n", ev.Traceparent)
					fmt.Printf("Failed At:      %s\n", ev.FailedAt)
					fmt.Println("Stack Trace:")
					fmt.Println(ev.StackTrace)
					fmt.Println("==================================================")
					if key != "" {
						break
					}
				}
			}
			if !found {
				fmt.Printf("No DLQ events found for key: %s\n", key)
			}
			return nil
		},
	}
	inspectCmd.Flags().StringVar(&topic, "topic", "papers.downloaded.dlq", "DLQ topic name")
	inspectCmd.Flags().StringVar(&key, "key", "", "Paper key to inspect")

	replayCmd := &cobra.Command{
		Use:   "replay",
		Short: "Republish DLQ events back to their original topics with attempt reset",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			mgr := dlq.NewManager(cfg.KafkaBrokers)
			count, err := mgr.Replay(ctx, topic, errorType, limit, dryRun)
			if err != nil {
				return err
			}

			fmt.Printf("Successfully processed replay for %d events (DryRun=%v)\n", count, dryRun)
			return nil
		},
	}
	replayCmd.Flags().StringVar(&topic, "topic", "papers.downloaded.dlq", "DLQ topic name")
	replayCmd.Flags().StringVar(&errorType, "error-type", "", "Filter by error type (e.g. PdfParseError)")
	replayCmd.Flags().IntVar(&limit, "limit", 100, "Max events to replay")
	replayCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Simulate replay without publishing")

	dlqCmd.AddCommand(listCmd)
	dlqCmd.AddCommand(inspectCmd)
	dlqCmd.AddCommand(replayCmd)
	return dlqCmd
}
