//go:build integration

package testutil

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// These match the s3.Config the suite constructs; pkg/s3.GetURL derives the
// virtual-hosted hostname (<bucket>.s3.<region>.amazonaws.com) from them.
const (
	S3Bucket = "test-bucket"
	S3Region = "us-east-1"
)

// s3VirtualHost is the hostname pkg/s3 builds for objects in the bucket. Both
// the reconciler's HEAD check and the vSphere OVA download target this host.
func s3VirtualHost() string {
	return fmt.Sprintf("%s.s3.%s.amazonaws.com", S3Bucket, S3Region)
}

// FakeS3 is an in-process S3-compatible server seeded with one or more objects.
type FakeS3 struct {
	backend gofakes3.Backend
	server  *httptest.Server
}

// StartFakeS3 boots gofakes3 configured for virtual-hosted-style addressing and
// seeds the bucket with object at objectKey. gofakes3 rewrites requests whose
// Host is a subdomain of the base into path-style bucket routing.
func StartFakeS3(objectKey string, object []byte) (*FakeS3, error) {
	backend := s3mem.New()
	faker := gofakes3.New(backend, gofakes3.WithHostBucketBase(fmt.Sprintf("s3.%s.amazonaws.com", S3Region)))

	if err := backend.CreateBucket(S3Bucket); err != nil {
		return nil, fmt.Errorf("failed to create fake bucket: %w", err)
	}

	f := &FakeS3{
		backend: backend,
		server:  httptest.NewServer(faker.Server()),
	}
	if err := f.Seed(objectKey, object); err != nil {
		return nil, err
	}
	return f, nil
}

// Seed adds another object to the bucket, letting a suite serve fixtures for
// more than one image key from the same fake server.
func (f *FakeS3) Seed(objectKey string, object []byte) error {
	if _, err := f.backend.PutObject(
		S3Bucket,
		objectKey,
		map[string]string{"Content-Type": "application/octet-stream"},
		bytes.NewReader(object),
		int64(len(object)),
		nil,
	); err != nil {
		return fmt.Errorf("failed to seed fake object %s: %w", objectKey, err)
	}
	return nil
}

// Close shuts down the fake server.
func (f *FakeS3) Close() {
	f.server.Close()
}

// RedirectS3Transport rewrites http.DefaultTransport so that any connection to
// the virtual-hosted S3 endpoint is dialed against the fake server instead,
// regardless of the (unspecified, i.e. :80/:443) port in the URL. This is what
// lets us keep pkg/s3.GetURL's hard-coded amazonaws.com hostname unchanged.
//
// It must be called BEFORE constructing the vSphere client: govmomi's soap
// client copies DialContext from http.DefaultTransport at construction time, so
// this patch is what routes its OVA download to the fake bucket. The reconciler's
// http.Head check uses http.DefaultClient and picks it up at request time.
//
// The returned function restores the original transport.
func RedirectS3Transport(f *FakeS3) func() {
	original := http.DefaultTransport
	patched := original.(*http.Transport).Clone()

	target := f.server.Listener.Addr().String()
	host := s3VirtualHost()
	dialer := &net.Dialer{}

	patched.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if h, _, err := net.SplitHostPort(addr); err == nil && h == host {
			return dialer.DialContext(ctx, network, target)
		}
		return dialer.DialContext(ctx, network, addr)
	}

	http.DefaultTransport = patched
	return func() { http.DefaultTransport = original }
}
