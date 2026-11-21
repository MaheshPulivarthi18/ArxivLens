package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"arxivlens/internal/config"
	"arxivlens/internal/crawler"
	"arxivlens/internal/db"
	"arxivlens/internal/dlq"
	"arxivlens/internal/downloader"
	"arxivlens/internal/kafka"
	"arxivlens/internal/llm"
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
			// QA-8: LLM synthesis when LLM_MODEL is set (LLM_BASE_URL, LLM_API_KEY, LLM_TIMEOUT, LLM_MAX_TOKENS);
			// otherwise extractive answers. A bad value is a startup error, never a silent default.
			provider, err := llm.FromEnv(os.Getenv)
			if err != nil {
				return fmt.Errorf("LLM config: %w", err)
			}
			if provider != nil {
				ragService.WithLLM(provider)
				log.Printf("[Query] LLM synthesis enabled (%s, model %s)", provider.Name(), os.Getenv("LLM_MODEL"))
			} else {
				log.Printf("[Query] LLM synthesis off (LLM_MODEL unset): extractive answers")
			}

			server := query.NewServer(database, searchEngine, ragService, port)

			minioStorage, err := storage.NewStorage(cfg.MinioEndpoint, cfg.MinioAccessKey, cfg.MinioSecretKey, cfg.MinioBucket, cfg.MinioUseSSL)
			if err == nil {
				ingester := query.NewPaperIngester(database, minioStorage.Client, cfg.MinioBucket, embedderURL, cfg.IngestTimeout)
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

// dlqReader is the DLQ topic reader the CLI builds per command; *dlq.KafkaReader in production, a fake in dlq_test.go.
type dlqReader interface {
	dlq.Reader
	Close()
}

func newDLQCmd() *cobra.Command {
	return newDLQCmdWith(func() (dlqReader, error) {
		r, err := dlq.NewKafkaReader(cfg.KafkaBrokers)
		if err != nil {
			return nil, err // not a typed nil in the interface
		}
		return r, nil
	})
}

// withDLQManager builds a reader, runs fn on a Manager that holds it, and closes the reader on every path.
// list and inspect only call List through here, so List never runs on a nil-reader Manager (OPS-H7a2 AC7).
func withDLQManager(newReader func() (dlqReader, error), fn func(context.Context, *dlq.Manager) error) error {
	r, err := newReader()
	if err != nil {
		return err
	}
	defer r.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return fn(ctx, dlq.NewManager(nil, r))
}

// printDLQRecord prints one record as P/O first (the form --from takes); an undecodable record is never hidden.
func printDLQRecord(w io.Writer, r dlq.Record) {
	ev := r.Payload
	if ev == nil {
		fmt.Fprintf(w, "%d/%d  UNDECODABLE (%d bytes)\n", r.Partition, r.Offset, len(r.Value))
		return
	}
	fmt.Fprintf(w, "%d/%d  key=%s  error=%s  failed_at=%s  -> %s\n", r.Partition, r.Offset, ev.OriginalKey, ev.ErrorType, ev.FailedAt, ev.OriginalTopic)
	fmt.Fprintf(w, "    message: %s\n", ev.ErrorMessage)
}

// watermarkList: "0=H0 1=H1 ..." in partition order.
func watermarkList(wm map[int32]dlq.Watermark) string {
	parts := make([]int32, 0, len(wm))
	for p := range wm {
		parts = append(parts, p)
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i] < parts[j] })
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = fmt.Sprintf("%d=%d", p, wm[p].High)
	}
	return strings.Join(out, " ")
}

func newDLQCmdWith(newReader func() (dlqReader, error)) *cobra.Command {
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
		partition int32
		from      string
		pageSize  int
		all       bool
	)

	listCmd := &cobra.Command{
		Use:          "list",
		Short:        "List failed events in a DLQ topic as P/O lines, a page at a time, up to the high watermarks at start",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opt := dlq.ListOptions{PageSize: pageSize}
			if cmd.Flags().Changed("partition") {
				opt.Partition = &partition
			}
			if from != "" {
				c, err := dlq.ParseCursor(from)
				if err != nil {
					return err
				}
				opt.From = c
			}
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			return withDLQManager(newReader, func(ctx context.Context, mgr *dlq.Manager) error {
				fmt.Fprintf(out, "=== DLQ topic: %s ===\n", topic)
				n := 0
				for {
					page, err := mgr.List(ctx, topic, opt)
					for _, w := range page.Warnings {
						fmt.Fprintln(errOut, w)
					}
					for _, r := range page.Records {
						printDLQRecord(out, r)
					}
					n += len(page.Records)
					if err != nil {
						return err // a stall or poll error is never "end of topic"
					}
					if !page.Next.IsSet() {
						fmt.Fprintf(out, "end of topic (high watermarks at start: %s; %d records)\n", watermarkList(page.Watermarks), n)
						return nil
					}
					if !all {
						fmt.Fprintf(out, "next page: arxivlens dlq list --topic %s --from %s\n", topic, page.Next)
						return nil
					}
					opt.From, opt.Snapshot = page.Next, page.Watermarks // --all: one start-time snapshot for the whole run
				}
			})
		},
	}
	listCmd.Flags().StringVar(&topic, "topic", "papers.downloaded.dlq", "DLQ topic name")
	listCmd.Flags().Int32Var(&partition, "partition", 0, "List only this partition")
	listCmd.Flags().StringVar(&from, "from", "", "Start at cursor P/O (partition P, offset O), as printed by the next-page footer")
	listCmd.Flags().IntVar(&pageSize, "page-size", dlq.DefaultPageSize, "Records per page")
	listCmd.Flags().IntVar(&pageSize, "limit", dlq.DefaultPageSize, "Alias of --page-size")
	_ = listCmd.Flags().MarkHidden("limit")
	listCmd.Flags().BoolVar(&all, "all", false, "Print every page to the end of the topic in one run")

	inspectCmd := &cobra.Command{
		Use:          "inspect",
		Short:        "Inspect full error diagnostic context and stack trace for a paper key (scans the whole topic)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			return withDLQManager(newReader, func(ctx context.Context, mgr *dlq.Manager) error {
				opt := dlq.ListOptions{PageSize: 500}
				for {
					page, err := mgr.List(ctx, topic, opt) // From is always >= the snapshot's Low here, so no expiry Warnings
					for _, r := range page.Records {
						ev := r.Payload
						if key != "" && (ev == nil || ev.OriginalKey != key) {
							continue
						}
						if ev == nil {
							printDLQRecord(out, r)
							continue
						}
						fmt.Fprintf(out, "%[1]s\nDLQ Record:     %d/%d\nOriginal Topic: %s (Partition %d, Offset %d)\nPaper Key:      %s\n"+
							"Error Type:     %s\nError Message:  %s\nAttempts:       %d\nConsumer Group: %s\nTraceparent:    %s\n"+
							"Failed At:      %s\nStack Trace:\n%s\n%[1]s\n", strings.Repeat("=", 50), r.Partition, r.Offset,
							ev.OriginalTopic, ev.Partition, ev.Offset, ev.OriginalKey, ev.ErrorType, ev.ErrorMessage, ev.Attempts,
							ev.ConsumerGroup, ev.Traceparent, ev.FailedAt, ev.StackTrace)
						if key != "" {
							return nil // first match
						}
					}
					if err != nil {
						return err
					}
					if !page.Next.IsSet() {
						break
					}
					opt.From, opt.Snapshot = page.Next, page.Watermarks // one snapshot for the whole scan
				}
				if key != "" {
					fmt.Fprintf(out, "No DLQ events found for key: %s\n", key)
				}
				return nil
			})
		},
	}
	inspectCmd.Flags().StringVar(&topic, "topic", "papers.downloaded.dlq", "DLQ topic name")
	inspectCmd.Flags().StringVar(&key, "key", "", "Paper key to inspect")

	replayCmd := &cobra.Command{
		Use:   "replay",
		Short: "Republish DLQ events back to their original topics with attempt reset",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			mgr := dlq.NewManager(cfg.KafkaBrokers, nil)
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
