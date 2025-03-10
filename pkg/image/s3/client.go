package s3

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-logr/logr"
)

// S3Client wraps the AWS SDK client
type Client struct {
	s3         s3.Client
	log        logr.Logger
	bucketName string
	directory  string
	timeout    time.Duration
}

type Config struct {
	Log        logr.Logger
	BucketName string
	Region     string
	Directory  string
	Timeout    time.Duration
}

// New initializes a new S3 client
func New(c Config, ctx context.Context) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(c.Region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg)
	return &Client{
		s3:         *client,
		log:        c.Log,
		bucketName: c.BucketName,
		directory:  c.Directory,
	}, nil
}

// Pull fetches an image from S3 and stores it locally
func (c *Client) Pull(ctx context.Context, imageKey string) (string, error) {
	c.log.Info(fmt.Sprintf("Starting to pull image %s from S3 bucket %s", imageKey, c.bucketName))

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
	defer resp.Body.Close()

	// Ensure local directory exists
	if err := os.MkdirAll(c.directory, os.ModePerm); err != nil {
		return "", fmt.Errorf("failed to ensure local directory %s.\n%w", c.directory, err)
	}

	// Define local file path
	localFilePath := filepath.Join(c.directory, filepath.Base(imageKey))
	file, err := os.Create(localFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to create local file %s.\n%w", localFilePath, err)
	}
	defer file.Close()

	// Stream data from S3 to file
	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to write S3 object to file%s.\n%w", localFilePath, err)
	}

	c.log.Info(fmt.Sprintf("Completed download of image %s to local path %s", imageKey, localFilePath))
	return localFilePath, nil
}
