package s3

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// S3Client wraps the AWS SDK client
type Client struct {
	s3         s3.Client
	protocol   string
	bucketName string
	region     string
	timeout    time.Duration
}

type Config struct {
	HTTP       bool
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

	// Set protocol based on HTTP flag
	protocol := "https"
	if c.HTTP {
		protocol = "http"
	}

	client := s3.NewFromConfig(cfg)
	return &Client{
		s3:         *client,
		bucketName: c.BucketName,
		timeout:    c.Timeout,
		region:     c.Region,
		protocol:   protocol,
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

// GetURL returns the URL of an image in S3
func (c *Client) GetURL(imageKey string) string {
	return fmt.Sprintf("%s://%s.s3.%s.amazonaws.com/%s", c.protocol, c.bucketName, c.region, imageKey)
}

// IsS3URL checks if a URL is an S3 URL
func (c *Client) IsS3URL(url string) bool {
	pattern := fmt.Sprintf(`^%s://[a-zA-Z0-9-]+\.s3\.[a-z0-9-]+\.amazonaws\.com/.+`, c.protocol)
	regexp := regexp.MustCompile(pattern)
	return regexp.MatchString(url)
}

func (c *Client) ValidURL(url string) error {
	if url == "" {
		return fmt.Errorf("URL is empty")
	}

	// Check that the URL is an s3 bucket
	if !c.IsS3URL(url) {
		return fmt.Errorf("URL is not an S3 bucket")
	}
	return nil
}
