package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Storage uploads opaque byte streams under a key and returns a URL that can
// later be used to fetch the object. Implementations are chosen at startup
// via the STORAGE_PROVIDER environment variable.
type Storage interface {
	Upload(ctx context.Context, key, contentType string, body io.Reader, size int64) (string, error)
	// Sign returns a URL the admin dashboard can use to fetch the object
	// referenced by key. If STORAGE_PUBLIC_URL_BASE is set the bucket is
	// assumed to be publicly readable and that URL is returned directly;
	// otherwise a presigned/short-lived URL is generated.
	Sign(ctx context.Context, key string, ttl time.Duration) (string, error)
}

func newStorage(ctx context.Context) (Storage, error) {
	provider := strings.ToLower(getEnv("STORAGE_PROVIDER", ""))
	bucket := getEnv("STORAGE_BUCKET", "")
	if bucket == "" {
		return nil, fmt.Errorf("STORAGE_BUCKET is required")
	}

	switch provider {
	case "s3":
		return newS3Storage(ctx, bucket)
	case "gcs":
		return newGCSStorage(ctx, bucket)
	default:
		return nil, fmt.Errorf("unsupported STORAGE_PROVIDER %q (want s3 or gcs)", provider)
	}
}

// --- S3 ---

type s3Storage struct {
	client       *s3.Client
	bucket       string
	publicPrefix string // optional override for the returned URL
}

func newS3Storage(ctx context.Context, bucket string) (*s3Storage, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	endpoint := getEnv("STORAGE_S3_ENDPOINT", "")
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = &endpoint
			o.UsePathStyle = true
		}
	})

	return &s3Storage{
		client:       client,
		bucket:       bucket,
		publicPrefix: strings.TrimRight(getEnv("STORAGE_PUBLIC_URL_BASE", ""), "/"),
	}, nil
}

func (s *s3Storage) Upload(ctx context.Context, key, contentType string, body io.Reader, size int64) (string, error) {
	contentLength := size
	in := &s3.PutObjectInput{
		Bucket:      &s.bucket,
		Key:         &key,
		Body:        body,
		ContentType: &contentType,
	}
	if contentLength > 0 {
		in.ContentLength = &contentLength
	}
	if _, err := s.client.PutObject(ctx, in); err != nil {
		return "", fmt.Errorf("s3 put: %w", err)
	}
	if s.publicPrefix != "" {
		return s.publicPrefix + "/" + key, nil
	}
	return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}

func (s *s3Storage) Sign(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if s.publicPrefix != "" {
		return s.publicPrefix + "/" + key, nil
	}
	presigner := s3.NewPresignClient(s.client)
	req, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &s.bucket,
		Key:    &key,
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("s3 presign: %w", err)
	}
	return req.URL, nil
}

// --- GCS ---

type gcsStorage struct {
	client       *storage.Client
	bucket       string
	publicPrefix string
}

func newGCSStorage(ctx context.Context, bucket string) (*gcsStorage, error) {
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcs client: %w", err)
	}
	return &gcsStorage{
		client:       client,
		bucket:       bucket,
		publicPrefix: strings.TrimRight(getEnv("STORAGE_PUBLIC_URL_BASE", ""), "/"),
	}, nil
}

func (g *gcsStorage) Upload(ctx context.Context, key, contentType string, body io.Reader, _ int64) (string, error) {
	w := g.client.Bucket(g.bucket).Object(key).NewWriter(ctx)
	w.ContentType = contentType
	if _, err := io.Copy(w, body); err != nil {
		_ = w.Close()
		return "", fmt.Errorf("gcs write: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("gcs close: %w", err)
	}
	if g.publicPrefix != "" {
		return g.publicPrefix + "/" + key, nil
	}
	return fmt.Sprintf("https://storage.googleapis.com/%s/%s", g.bucket, url.PathEscape(key)), nil
}

func (g *gcsStorage) Sign(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if g.publicPrefix != "" {
		return g.publicPrefix + "/" + key, nil
	}
	// Relies on Application Default Credentials with iam.serviceAccounts.signBlob
	// permission OR a service-account JSON key.
	signed, err := g.client.Bucket(g.bucket).SignedURL(key, &storage.SignedURLOptions{
		Method:  "GET",
		Expires: time.Now().Add(ttl),
		Scheme:  storage.SigningSchemeV4,
	})
	if err != nil {
		return "", fmt.Errorf("gcs sign: %w", err)
	}
	return signed, nil
}
