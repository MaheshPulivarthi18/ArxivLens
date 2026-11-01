package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Storage struct {
	Client *minio.Client
	Bucket string
}

func NewStorage(endpoint, accessKey, secretKey, bucket string, useSSL bool) (*Storage, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create minio client: %w", err)
	}

	return &Storage{
		Client: client,
		Bucket: bucket,
	}, nil
}

func (s *Storage) EnsureBucket(ctx context.Context) error {
	exists, err := s.Client.BucketExists(ctx, s.Bucket)
	if err != nil {
		return fmt.Errorf("failed to check bucket existence: %w", err)
	}
	if !exists {
		err = s.Client.MakeBucket(ctx, s.Bucket, minio.MakeBucketOptions{})
		if err != nil {
			return fmt.Errorf("failed to create bucket %s: %w", s.Bucket, err)
		}
	}
	return nil
}

func (s *Storage) UploadPDF(ctx context.Context, objectKey string, reader io.Reader, size int64) error {
	_, err := s.Client.PutObject(ctx, s.Bucket, objectKey, reader, size, minio.PutObjectOptions{
		ContentType: "application/pdf",
	})
	if err != nil {
		return fmt.Errorf("failed to upload object %s to minio: %w", objectKey, err)
	}
	return nil
}

func (s *Storage) DownloadPDF(ctx context.Context, objectKey string) (io.ReadCloser, error) {
	obj, err := s.Client.GetObject(ctx, s.Bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get object %s from minio: %w", objectKey, err)
	}
	return obj, nil
}

func (s *Storage) ObjectExists(ctx context.Context, objectKey string) (bool, error) {
	_, err := s.Client.StatObject(ctx, s.Bucket, objectKey, minio.StatObjectOptions{})
	if err != nil {
		errResp := minio.ToErrorResponse(err)
		if errResp.Code == "NoSuchKey" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
