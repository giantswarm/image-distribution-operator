package image

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	images "github.com/giantswarm/image-distribution-operator/api/image/v1alpha1"
)

func TestRemoveImage(t *testing.T) {
	testCases := []struct {
		name          string
		existingImage *images.NodeImage
		release       string

		expectedError    error
		expectDeleted    bool
		expectedReleases []string
	}{
		{
			name:    "case 0: image with single release is deleted",
			release: "v1.0.0",
			existingImage: &images.NodeImage{
				ObjectMeta: metav1.ObjectMeta{Name: "test-image", Namespace: "test-namespace"},
				Status:     images.NodeImageStatus{Releases: []string{"v1.0.0"}},
			},
			expectDeleted: true,
		},
		{
			name:    "case 1: image with multiple releases is updated",
			release: "v1.0.0",
			existingImage: &images.NodeImage{
				ObjectMeta: metav1.ObjectMeta{Name: "test-image", Namespace: "test-namespace"},
				Status:     images.NodeImageStatus{Releases: []string{"v1.0.0", "v1.1.0"}},
			},
			expectDeleted:    false,
			expectedReleases: []string{"v1.1.0"},
		},
		{
			name:          "case 2: image does not exist (no error)",
			release:       "v1.0.0",
			existingImage: nil, // No pre-existing image
			expectDeleted: true,
		},
	}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("case %d", i), func(t *testing.T) {
			ctx := context.TODO()

			var fakeClient client.Client
			{
				scheme := runtime.NewScheme()
				err := images.AddToScheme(scheme)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				fakeClient = fake.NewClientBuilder().
					WithScheme(scheme).
					WithStatusSubresource(&images.NodeImage{}).
					Build()
			}

			if tc.existingImage != nil {
				err := fakeClient.Create(ctx, tc.existingImage)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			c, err := New(Config{
				Client:    fakeClient,
				Namespace: "test-namespace",
				Release:   tc.release,
			})
			assert.NoError(t, err)

			err = c.RemoveReleaseFromNodeImageStatus(ctx, "test-image")
			assert.Equal(t, tc.expectedError, err)

			err = c.DeleteImage(ctx, "test-image")
			assert.Equal(t, tc.expectedError, err)

			fetchedImage := &images.NodeImage{}
			err = fakeClient.Get(ctx, client.ObjectKey{Name: "test-image", Namespace: "test-namespace"}, fetchedImage)

			if tc.expectDeleted {
				assert.Error(t, err) // Should be deleted
			} else {
				assert.NoError(t, err) // Should still exist
				assert.ElementsMatch(t, tc.expectedReleases, fetchedImage.Status.Releases)
			}
		})
	}
}

func TestCreateOrUpdateImage(t *testing.T) {
	testCases := []struct {
		name          string
		existingImage *images.NodeImage
		release       string

		expectedError    error
		expectedCreated  bool
		expectedReleases []string
	}{
		{
			name:             "case 0: image does not exist, should be created",
			release:          "v1.0.0",
			existingImage:    nil, // No pre-existing image
			expectedCreated:  true,
			expectedReleases: []string{"v1.0.0"}, // status not yet set
		},
		{
			name:    "case 1: image exists but does not have release, should add release",
			release: "v1.0.0",
			existingImage: &images.NodeImage{
				ObjectMeta: metav1.ObjectMeta{Name: "test-image", Namespace: "test-namespace"},
				Status:     images.NodeImageStatus{Releases: []string{}},
			},
			expectedCreated:  false,
			expectedReleases: []string{"v1.0.0"},
		},
		{
			name:    "case 2: image already contains release, should not duplicate",
			release: "v1.0.0",
			existingImage: &images.NodeImage{
				ObjectMeta: metav1.ObjectMeta{Name: "test-image", Namespace: "test-namespace"},
				Status:     images.NodeImageStatus{Releases: []string{"v1.0.0"}},
			},
			expectedCreated:  false,
			expectedReleases: []string{"v1.0.0"}, // Should not duplicate
		},
		{
			name:    "case 3: image already contains multiple releases, should add release",
			release: "v1.0.0",
			existingImage: &images.NodeImage{
				ObjectMeta: metav1.ObjectMeta{Name: "test-image", Namespace: "test-namespace"},
				Status:     images.NodeImageStatus{Releases: []string{"v1.1.0", "v1.2.0"}},
			},
			expectedCreated:  false,
			expectedReleases: []string{"v1.1.0", "v1.2.0", "v1.0.0"},
		},
	}

	for i, tc := range testCases {
		t.Run(fmt.Sprintf("case %d", i), func(t *testing.T) {
			ctx := context.TODO()

			var fakeClient client.Client
			{
				scheme := runtime.NewScheme()
				err := images.AddToScheme(scheme)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				fakeClient = fake.NewClientBuilder().
					WithScheme(scheme).
					WithStatusSubresource(&images.NodeImage{}).
					Build()
			}

			if tc.existingImage != nil {
				err := fakeClient.Create(ctx, tc.existingImage)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				err = fakeClient.Status().Update(ctx, tc.existingImage)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			c, err := New(Config{
				Client:    fakeClient,
				Namespace: "test-namespace",
				Release:   tc.release,
			})
			assert.NoError(t, err)

			image := &images.NodeImage{
				ObjectMeta: metav1.ObjectMeta{Name: "test-image", Namespace: "test-namespace"},
			}

			err = c.CreateImage(ctx, image)
			assert.Equal(t, tc.expectedError, err)

			err = c.AddReleaseToNodeImageStatus(ctx, "test-image")
			assert.Equal(t, tc.expectedError, err)

			fetchedImage := &images.NodeImage{}
			err = fakeClient.Get(ctx, client.ObjectKey{Name: "test-image", Namespace: "test-namespace"}, fetchedImage)

			if tc.expectedCreated {
				assert.NoError(t, err) // Should exist
			}

			assert.ElementsMatch(t, tc.expectedReleases, fetchedImage.Status.Releases)
		})
	}
}
