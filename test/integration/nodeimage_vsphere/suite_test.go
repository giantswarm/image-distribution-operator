//go:build integration

/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nodeimagevsphere

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	imagev1alpha1 "github.com/giantswarm/image-distribution-operator/api/image/v1alpha1"
	imagectrl "github.com/giantswarm/image-distribution-operator/internal/controller/image"
	imagekey "github.com/giantswarm/image-distribution-operator/pkg/image"
	"github.com/giantswarm/image-distribution-operator/pkg/provider"
	"github.com/giantswarm/image-distribution-operator/pkg/s3"
	"github.com/giantswarm/image-distribution-operator/pkg/vsphere"
	"github.com/giantswarm/image-distribution-operator/test/integration/testutil"
	"github.com/giantswarm/image-distribution-operator/test/utils"
)

const (
	testProvider     = "vsphere"
	testResourceName = "vsphere-test-image"
	// testImageName is the value placed in NodeImage.Spec.Name. pkg/image derives
	// the S3 object key (and thus the fixture we seed) from it.
	testImageName = "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs"

	// The deletion test uses a distinct resource/image so it imports and destroys
	// its own vcsim template, independent of the create test's inventory.
	testDeleteResourceName = "vsphere-test-image-delete"
	testDeleteImageName    = "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-delete-gs"

	// Ginkgo randomizes spec order, so every spec below owns a distinct
	// resource/image name to keep its vcsim inventory independent.

	// Idempotency test: valid OVA seeded, imported then re-reconciled.
	testIdempotentResourceName = "vsphere-test-image-idempotent"
	testIdempotentImageName    = "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-idempotent-gs"

	// Missing test: deliberately NOT seeded into the fake bucket.
	testMissingResourceName = "vsphere-test-image-missing"
	testMissingImageName    = "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-missing-gs"

	// Error test: seeded with garbage bytes so the OVF import fails.
	testErrorResourceName = "vsphere-test-image-error"
	testErrorImageName    = "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-error-gs"
)

var (
	ctx    context.Context
	cancel context.CancelFunc

	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client

	vcsim         *testutil.VCSim
	fakeS3        *testutil.FakeS3
	restoreS3     func()
	tempDir       string
	reconciler    *imagectrl.NodeImageReconciler
	vsphereClient *vsphere.Client
)

func TestIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "NodeImage vSphere Integration Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	Expect(imagev1alpha1.AddToScheme(scheme.Scheme)).To(Succeed())

	By("bootstrapping the test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	utils.GetEnvOrSkip("KUBEBUILDER_ASSETS")

	if dir := firstFoundEnvTestBinaryDir(); dir != "" {
		testEnv.BinaryAssetsDirectory = dir
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	By("starting the in-process vSphere simulator")
	vcsim, err = testutil.StartVCSim()
	Expect(err).NotTo(HaveOccurred())

	By("seeding the in-process S3 bucket with an OVA fixture")
	imageKey := imagekey.GetImageKey(&imagev1alpha1.NodeImage{
		Spec: imagev1alpha1.NodeImageSpec{Name: testImageName, Provider: testProvider},
	})
	ova, err := testutil.BuildOVA()
	Expect(err).NotTo(HaveOccurred())
	fakeS3, err = testutil.StartFakeS3(imageKey, ova)
	Expect(err).NotTo(HaveOccurred())

	deleteImageKey := imagekey.GetImageKey(&imagev1alpha1.NodeImage{
		Spec: imagev1alpha1.NodeImageSpec{Name: testDeleteImageName, Provider: testProvider},
	})
	Expect(fakeS3.Seed(deleteImageKey, ova)).To(Succeed())

	idempotentImageKey := imagekey.GetImageKey(&imagev1alpha1.NodeImage{
		Spec: imagev1alpha1.NodeImageSpec{Name: testIdempotentImageName, Provider: testProvider},
	})
	Expect(fakeS3.Seed(idempotentImageKey, ova)).To(Succeed())

	// Seeded with bytes that are not a valid OVA so the vSphere import fails.
	errorImageKey := imagekey.GetImageKey(&imagev1alpha1.NodeImage{
		Spec: imagev1alpha1.NodeImageSpec{Name: testErrorImageName, Provider: testProvider},
	})
	Expect(fakeS3.Seed(errorImageKey, []byte("not a valid ova"))).To(Succeed())

	// testMissingImageName is intentionally left unseeded.

	// Must run before constructing the vSphere client: govmomi's soap client
	// inherits DialContext from http.DefaultTransport at construction time.
	restoreS3 = testutil.RedirectS3Transport(fakeS3)

	By("constructing the real vSphere provider and S3 client")
	tempDir, err = os.MkdirTemp("", "idop-integration-")
	Expect(err).NotTo(HaveOccurred())
	credentialsPath, locationsPath, err := vcsim.WriteConfig(tempDir)
	Expect(err).NotTo(HaveOccurred())

	vsphereClient, err = vsphere.New(vsphere.Config{
		Backoff:         wait.Backoff{Steps: 5, Duration: 100 * time.Millisecond, Factor: 1.0},
		CredentialsFile: credentialsPath,
		LocationsFile:   locationsPath,
		PullMode:        false,
	}, ctx)
	Expect(err).NotTo(HaveOccurred())

	s3Client, err := s3.New(s3.Config{
		HTTP:       true,
		BucketName: testutil.S3Bucket,
		Region:     testutil.S3Region,
		Timeout:    30 * time.Second,
	}, ctx)
	Expect(err).NotTo(HaveOccurred())

	reconciler = &imagectrl.NodeImageReconciler{
		Client:    k8sClient,
		S3Client:  s3Client,
		Providers: map[string]provider.Provider{testProvider: vsphereClient},
	}
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	if restoreS3 != nil {
		restoreS3()
	}
	if fakeS3 != nil {
		fakeS3.Close()
	}
	if vcsim != nil {
		vcsim.Close()
	}
	if tempDir != "" {
		Expect(os.RemoveAll(tempDir)).To(Succeed())
	}
	cancel()
	Expect(testEnv.Stop()).To(Succeed())
})

// firstFoundEnvTestBinaryDir mirrors the helper in the controller suites so the
// test can also be run from an IDE after `make setup-envtest`.
func firstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
