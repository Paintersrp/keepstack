package main

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/example/keepstack/apps/api/internal/config"
	"github.com/example/keepstack/apps/api/internal/digest"
	"github.com/example/keepstack/apps/api/internal/resurfacer"
	"github.com/example/keepstack/apps/api/internal/schema"
)

func main() {
	logger := log.New(os.Stdout, "keepstack-cron ", log.LstdFlags|log.LUTC)

	if len(os.Args) < 2 {
		logger.Fatalf("expected subcommand (available: digest)")
	}

	switch os.Args[1] {
	case "digest":
		if err := runDigest(logger); err != nil {
			if errors.Is(err, digest.ErrNoUnreadLinks) {
				logger.Println("no unread links, skipping digest dispatch")
				return
			}
			logger.Fatalf("digest run failed: %v", err)
		}
	case "verify-schema":
		if err := runVerifySchema(logger); err != nil {
			logger.Fatalf("schema verification failed: %v", err)
		}
	case "backup":
		if err := runBackup(logger); err != nil {
			logger.Fatalf("backup failed: %v", err)
		}
	case "resurface":
		if err := runResurface(logger); err != nil {
			logger.Fatalf("resurface run failed: %v", err)
		}
	default:
		logger.Fatalf("unknown subcommand %q", os.Args[1])
	}
}

func runDigest(logger *log.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	digestCfg, err := digest.LoadConfig()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	svc, err := digest.New(pool, digestCfg)
	if err != nil {
		return err
	}

	count, err := svc.Send(ctx, cfg.DevUserID)
	if err != nil {
		return err
	}

	logger.Printf("sent digest with %d unread links", count)
	return nil
}

func runVerifySchema(logger *log.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := schema.Verify(ctx, pool); err != nil {
		return err
	}

	logger.Println("database schema verified")
	return nil
}

func runBackup(logger *log.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	destDir := getEnvDefault("BACKUP_DIR", "/backups")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}

	retention := getEnvInt("BACKUP_RETENTION", 7)
	storage := strings.ToLower(getEnvDefault("BACKUP_STORAGE", "pvc"))
	now := time.Now().UTC()
	fileName := fmt.Sprintf("keepstack-%s.sql.gz", now.Format("20060102-150405"))
	path := filepath.Join(destDir, fileName)

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create dump file: %w", err)
	}
	gzipWriter := gzip.NewWriter(file)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "pg_dump", cfg.DatabaseURL)
	cmd.Stdout = gzipWriter
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %w", err)
	}

	if err := gzipWriter.Close(); err != nil {
		return fmt.Errorf("close gzip writer: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close dump file: %w", err)
	}

	if storage == "s3" {
		if err := uploadBackupToS3(ctx, path, fileName); err != nil {
			return err
		}
		if strings.ToLower(getEnvDefault("BACKUP_KEEP_LOCAL", "false")) != "true" {
			if err := os.Remove(path); err != nil {
				logger.Printf("warn: failed to remove local backup after upload: %v", err)
			}
		}
	}

	if retention > 0 {
		pattern := filepath.Join(destDir, "keepstack-*.sql.gz")
		files, err := filepath.Glob(pattern)
		if err == nil {
			sort.Strings(files)
			for len(files) > retention {
				old := files[0]
				if rmErr := os.Remove(old); rmErr != nil {
					logger.Printf("warn: failed to remove old backup %s: %v", old, rmErr)
				}
				files = files[1:]
			}
		}
	}

	logger.Printf("backup written to %s", path)
	return nil
}

func uploadBackupToS3(ctx context.Context, path, fileName string) error {
	bucket := getEnvDefault("BACKUP_S3_BUCKET", "")
	accessKey := getEnvDefault("BACKUP_S3_ACCESS_KEY", "")
	secretKey := getEnvDefault("BACKUP_S3_SECRET_KEY", "")
	region := getEnvDefault("BACKUP_S3_REGION", "us-east-1")
	endpoint := getEnvDefault("BACKUP_S3_ENDPOINT", "")
	prefix := strings.TrimSuffix(getEnvDefault("BACKUP_S3_PREFIX", ""), "/")

	if bucket == "" || accessKey == "" || secretKey == "" {
		return fmt.Errorf("missing S3 configuration")
	}

	key := fileName
	if prefix != "" {
		key = fmt.Sprintf("%s/%s", prefix, fileName)
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open backup for upload: %w", err)
	}
	defer file.Close()

	cfg, err := awsConfig(ctx, region, accessKey, secretKey, endpoint)
	if err != nil {
		return fmt.Errorf("configure s3 client: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        file,
		ContentType: aws.String("application/gzip"),
	})
	if err != nil {
		return fmt.Errorf("upload backup: %w", err)
	}

	return nil
}

func awsConfig(ctx context.Context, region, accessKey, secretKey, endpoint string) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	}

	if endpoint != "" {
		resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, _ ...interface{}) (aws.Endpoint, error) {
			return aws.Endpoint{
				URL:               endpoint,
				HostnameImmutable: true,
			}, nil
		})
		opts = append(opts, awsconfig.WithEndpointResolverWithOptions(resolver))
	}

	return awsconfig.LoadDefaultConfig(ctx, opts...)
}

func runResurface(logger *log.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	limit := getEnvInt("RESURFACER_LIMIT", 20)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	svc := resurfacer.New(pool)
	count, err := svc.Rebuild(ctx, limit)
	if err != nil {
		return err
	}

	logger.Printf("refreshed %d recommendations", count)
	return nil
}

func getEnvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	if value < 0 {
		return 0
	}
	return value
}
