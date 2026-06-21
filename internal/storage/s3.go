// Package storage uploads completed recordings to an S3-compatible object
// store, optionally through an HTTP/HTTPS proxy.
package storage

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/AkagiYui/kenko-nvr/internal/database"
)

// Uploader uploads files to an S3 bucket.
type Uploader struct {
	client    *minio.Client
	bucket    string
	keyPrefix string
}

// NewUploader builds an S3 uploader from config. When cfg.ProxyURL is set, all
// S3 traffic is routed through that HTTP proxy (CONNECT tunneling is used
// automatically for HTTPS endpoints).
func NewUploader(cfg database.S3Config) (*Uploader, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("s3 endpoint is empty")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3 bucket is empty")
	}

	transport := &http.Transport{
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if cfg.ProxyURL != "" {
		pu, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy url: %w", err)
		}
		transport.Proxy = http.ProxyURL(pu)
	} else {
		transport.Proxy = http.ProxyFromEnvironment
	}

	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:    cfg.UseSSL,
		Region:    cfg.Region,
		Transport: transport,
	})
	if err != nil {
		return nil, fmt.Errorf("creating s3 client: %w", err)
	}

	return &Uploader{
		client:    client,
		bucket:    cfg.Bucket,
		keyPrefix: strings.Trim(cfg.KeyPrefix, "/"),
	}, nil
}

// Key derives the object key for a recording's relative path.
func (u *Uploader) Key(relPath string) string {
	relPath = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(relPath, "\\", "/")), "/")
	if u.keyPrefix == "" {
		return relPath
	}
	return u.keyPrefix + "/" + relPath
}

// Upload uploads localPath to the bucket under the given key.
func (u *Uploader) Upload(ctx context.Context, localPath, key string) error {
	_, err := u.client.FPutObject(ctx, u.bucket, key, localPath, minio.PutObjectOptions{
		ContentType: "video/mp4",
	})
	if err != nil {
		return fmt.Errorf("uploading %q: %w", key, err)
	}
	return nil
}

// CheckBucket verifies the bucket is reachable and exists. Useful for the
// settings "test connection" action.
func (u *Uploader) CheckBucket(ctx context.Context) error {
	ok, err := u.client.BucketExists(ctx, u.bucket)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("bucket %q does not exist", u.bucket)
	}
	return nil
}
