package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// S3Client wraps the AWS SDK client
type Client struct {
	s3         s3.Client
	bucketName string
	timeout    time.Duration
}

type Config struct {
	BucketName string
	Region     string
	Timeout    time.Duration
}

const (
	Directory = "/tmp/images"
)

// New initializes a new S3 client
func New(c Config, ctx context.Context) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(c.Region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg)
	return &Client{
		s3:         *client,
		bucketName: c.BucketName,
		timeout:    c.Timeout,
	}, nil
}

// Pull fetches an image from S3 and stores it locally
func (c *Client) Pull(ctx context.Context, imageKey string) (string, error) {
	log := log.FromContext(ctx)

	log.Info("Starting to pull image from S3", "imageKey", imageKey, "bucketName", c.bucketName)

	// Set timeout
	childCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Fetch image from S3
	resp, err := c.s3.GetObject(childCtx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucketName),
		Key:    aws.String(imageKey),
	})
	if err != nil {
		return "", fmt.Errorf("failed to pull image %s from S3 bucket %s.\n%w", imageKey, c.bucketName, err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Error(err, "failed to close S3 response body")
		}
	}()

	// Ensure local directory exists
	if err := os.MkdirAll(Directory, 0700); err != nil {
		return "", fmt.Errorf("failed to ensure local directory %s.\n%w", Directory, err)
	}

	// Define local file path
	localFilePath := filepath.Join(Directory, filepath.Base(imageKey))

	file, err := os.Create(localFilePath) //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("failed to create local file %s.\n%w", localFilePath, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Error(err, "failed to close local file")
		}
	}()

	// Stream data from S3 to file
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to write S3 object to file%s.\n%w", localFilePath, err)
	}

	log.Info("Completed download of image from S3", "imageKey", imageKey, "localFilePath", localFilePath)
	return localFilePath, nil
}

// PullNoAuth fetches an image from S3 without authentication
func (c *Client) PullNoAuth(ctx context.Context, imageKey string) (string, error) {
	log := log.FromContext(ctx)

	imageURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", c.bucketName, "eu-central-1", imageKey)
	log.Info("Downloading image", "url", imageURL)

	resp, err := http.Get(imageURL)
	if err != nil {
		return "", fmt.Errorf("failed to fetch image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download image, status code: %d", resp.StatusCode)
	}

	if err := os.MkdirAll(Directory, 0700); err != nil {
		return "", fmt.Errorf("failed to create local directory %s: %w", Directory, err)
	}

	localFilePath := filepath.Join(Directory, filepath.Base(imageKey))
	file, err := os.Create(localFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to create local file: %w", err)
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to write image to file: %w", err)
	}

	log.Info("Image downloaded successfully", "path", localFilePath)
	return localFilePath, nil
}
