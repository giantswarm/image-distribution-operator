package image

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	imagev1alpha1 "github.com/giantswarm/image-distribution-operator/api/image/v1alpha1"
	"github.com/giantswarm/image-distribution-operator/pkg/image"
	"github.com/giantswarm/image-distribution-operator/pkg/provider"
)

// fakeProvider is an in-memory provider.Provider with a single location.
type fakeProvider struct {
	// exists and existsErr are returned from Exists.
	exists    bool
	existsErr error
	// createErr is returned from Create. A successful Create makes the image
	// healthy for later Exists calls.
	createErr error
	deleteErr error
	// onCreate runs at the start of Create, before createErr is returned.
	onCreate func()

	createCalls int
	deleteCalls int
}

func (p *fakeProvider) Exists(_ context.Context, _ string, _ string) (bool, error) {
	return p.exists, p.existsErr
}

func (p *fakeProvider) Create(_ context.Context, _ string, _ string, _ string) error {
	p.createCalls++
	if p.onCreate != nil {
		p.onCreate()
	}
	if p.createErr != nil {
		return p.createErr
	}
	p.exists, p.existsErr = true, nil
	return nil
}

func (p *fakeProvider) Delete(_ context.Context, _ string, _ string) error {
	p.deleteCalls++
	if p.deleteErr != nil {
		return p.deleteErr
	}
	p.exists, p.existsErr = false, nil
	return nil
}

func (p *fakeProvider) GetLocations() map[string]interface{} {
	return map[string]interface{}{"loc": nil}
}

func brokenErr() error {
	return fmt.Errorf("vApp template has status FAILED_CREATION: %w", provider.ErrImageBroken)
}

type uploadAttemptsFixture struct {
	reconciler *NodeImageReconciler
	client     client.Client
	key        client.ObjectKey
	url        string
}

func newUploadAttemptsFixture(t *testing.T, annotations map[string]string, state imagev1alpha1.NodeImageState) *uploadAttemptsFixture {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, imagev1alpha1.AddToScheme(scheme))

	nodeImage := &imagev1alpha1.NodeImage{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "capvcd-flatcar",
			Namespace:   "default",
			Annotations: annotations,
		},
		Spec: imagev1alpha1.NodeImageSpec{Name: "flatcar", Provider: "capvcd"},
		Status: imagev1alpha1.NodeImageStatus{
			Releases: []string{"cloud-director-1.2.3"},
			State:    state,
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(nodeImage).
		WithStatusSubresource(nodeImage).
		Build()

	// The reconciler HEADs the image URL before touching the provider.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	return &uploadAttemptsFixture{
		reconciler: &NodeImageReconciler{Client: c},
		client:     c,
		key:        client.ObjectKeyFromObject(nodeImage),
		url:        server.URL,
	}
}

func (f *uploadAttemptsFixture) get(t *testing.T) *imagev1alpha1.NodeImage {
	t.Helper()
	nodeImage := &imagev1alpha1.NodeImage{}
	require.NoError(t, f.client.Get(context.Background(), f.key, nodeImage))
	return nodeImage
}

// reconcile runs the provider part of the reconcile loop on a freshly fetched
// object, like a real reconcile would.
func (f *uploadAttemptsFixture) reconcile(t *testing.T, prov provider.Provider) (bool, error) {
	t.Helper()
	res, err := f.reconciler.reconcileProvider(context.Background(), f.get(t), f.url, prov)
	return res.RequeueAfter == DefaultRequeue().RequeueAfter, err
}

func TestBrokenImageIsDeletedAndReuploaded(t *testing.T) {
	f := newUploadAttemptsFixture(t, nil, imagev1alpha1.NodeImageAvailable)
	prov := &fakeProvider{existsErr: brokenErr()}

	// The attempt must be recorded before the upload starts, so an upload that
	// is interrupted (e.g. the pod is evicted) still counts.
	var attemptsDuringCreate string
	var stateDuringCreate imagev1alpha1.NodeImageState
	prov.onCreate = func() {
		current := f.get(t)
		attemptsDuringCreate = current.Annotations[image.UploadAttemptsAnnotation]
		stateDuringCreate = current.Status.State
	}

	requeued, err := f.reconcile(t, prov)
	require.NoError(t, err)
	assert.True(t, requeued)

	assert.Equal(t, 1, prov.deleteCalls)
	assert.Equal(t, 1, prov.createCalls)
	assert.Equal(t, "1", attemptsDuringCreate)
	assert.Equal(t, imagev1alpha1.NodeImageUploading, stateDuringCreate)

	nodeImage := f.get(t)
	assert.Equal(t, imagev1alpha1.NodeImageAvailable, nodeImage.Status.State)
	assert.Equal(t, []string{"cloud-director-1.2.3"}, nodeImage.Status.Releases)
	// Not reset yet: the upload has not been confirmed healthy by Exists.
	assert.Equal(t, "1", nodeImage.Annotations[image.UploadAttemptsAnnotation])

	msg := "a subsequent reconcile confirms the image and resets the counter"
	requeued, err = f.reconcile(t, prov)
	require.NoError(t, err, msg)
	assert.True(t, requeued, msg)
	assert.Equal(t, 1, prov.createCalls, msg)
	nodeImage = f.get(t)
	assert.Equal(t, imagev1alpha1.NodeImageAvailable, nodeImage.Status.State, msg)
	assert.NotContains(t, nodeImage.Annotations, image.UploadAttemptsAnnotation, msg)
}

func TestUploadAttemptsAreBounded(t *testing.T) {
	f := newUploadAttemptsFixture(t, nil, imagev1alpha1.NodeImagePending)
	prov := &fakeProvider{createErr: errors.New("upload failed")}

	for attempt := 1; attempt <= maxUploadAttempts; attempt++ {
		_, err := f.reconcile(t, prov)
		require.Error(t, err, "attempt %d", attempt)

		nodeImage := f.get(t)
		assert.Equal(t, imagev1alpha1.NodeImageError, nodeImage.Status.State, "attempt %d", attempt)
		assert.Equal(t, fmt.Sprint(attempt), nodeImage.Annotations[image.UploadAttemptsAnnotation], "attempt %d", attempt)
	}
	assert.Equal(t, maxUploadAttempts, prov.createCalls)

	// Once exhausted, no further upload is attempted and the NodeImage is
	// requeued with the default interval instead of backing off on an error.
	for range 2 {
		requeued, err := f.reconcile(t, prov)
		require.NoError(t, err)
		assert.True(t, requeued)
	}
	assert.Equal(t, maxUploadAttempts, prov.createCalls)

	nodeImage := f.get(t)
	assert.Equal(t, imagev1alpha1.NodeImageError, nodeImage.Status.State)
	assert.Equal(t, fmt.Sprint(maxUploadAttempts), nodeImage.Annotations[image.UploadAttemptsAnnotation])
}

func TestBrokenImageIsKeptOnceAttemptsAreExhausted(t *testing.T) {
	f := newUploadAttemptsFixture(t, map[string]string{
		image.UploadAttemptsAnnotation: fmt.Sprint(maxUploadAttempts),
	}, imagev1alpha1.NodeImageAvailable)
	prov := &fakeProvider{existsErr: brokenErr()}

	requeued, err := f.reconcile(t, prov)
	require.NoError(t, err)
	assert.True(t, requeued)

	assert.Equal(t, 0, prov.deleteCalls)
	assert.Equal(t, 0, prov.createCalls)
	assert.Equal(t, imagev1alpha1.NodeImageError, f.get(t).Status.State)
}

func TestRemovingTheAnnotationAllowsANewUpload(t *testing.T) {
	f := newUploadAttemptsFixture(t, map[string]string{
		image.UploadAttemptsAnnotation: fmt.Sprint(maxUploadAttempts),
	}, imagev1alpha1.NodeImageError)
	prov := &fakeProvider{existsErr: brokenErr()}

	nodeImage := f.get(t)
	delete(nodeImage.Annotations, image.UploadAttemptsAnnotation)
	require.NoError(t, f.client.Update(context.Background(), nodeImage))

	_, err := f.reconcile(t, prov)
	require.NoError(t, err)

	assert.Equal(t, 1, prov.deleteCalls)
	assert.Equal(t, 1, prov.createCalls)
	assert.Equal(t, imagev1alpha1.NodeImageAvailable, f.get(t).Status.State)
}

func TestHealthyImageResetsUploadAttempts(t *testing.T) {
	f := newUploadAttemptsFixture(t, map[string]string{
		image.UploadAttemptsAnnotation: "2",
		image.LastUsedAnnotation:       "keep-me",
	}, imagev1alpha1.NodeImageError)
	prov := &fakeProvider{exists: true}

	requeued, err := f.reconcile(t, prov)
	require.NoError(t, err)
	assert.True(t, requeued)

	assert.Equal(t, 0, prov.createCalls)
	assert.Equal(t, 0, prov.deleteCalls)

	nodeImage := f.get(t)
	assert.Equal(t, imagev1alpha1.NodeImageAvailable, nodeImage.Status.State)
	assert.Equal(t, []string{"cloud-director-1.2.3"}, nodeImage.Status.Releases)
	assert.NotContains(t, nodeImage.Annotations, image.UploadAttemptsAnnotation)
	assert.Equal(t, "keep-me", nodeImage.Annotations[image.LastUsedAnnotation])
}

func TestExistsErrorDoesNotConsumeAnAttempt(t *testing.T) {
	f := newUploadAttemptsFixture(t, nil, imagev1alpha1.NodeImageAvailable)
	prov := &fakeProvider{existsErr: errors.New("connection refused")}

	_, err := f.reconcile(t, prov)
	require.Error(t, err)

	assert.Equal(t, 0, prov.createCalls)
	assert.Equal(t, 0, prov.deleteCalls)
	nodeImage := f.get(t)
	assert.Equal(t, imagev1alpha1.NodeImageError, nodeImage.Status.State)
	assert.NotContains(t, nodeImage.Annotations, image.UploadAttemptsAnnotation)
}
