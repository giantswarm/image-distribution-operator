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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/vim25/mo"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	imagev1alpha1 "github.com/giantswarm/image-distribution-operator/api/image/v1alpha1"
	"github.com/giantswarm/image-distribution-operator/test/integration/testutil"
)

var _ = Describe("NodeImage vSphere reconciliation", func() {
	It("imports the S3 image into vSphere as a template", func() {
		namespacedName := types.NamespacedName{Name: testResourceName, Namespace: "default"}

		By("creating the NodeImage resource")
		nodeImage := &imagev1alpha1.NodeImage{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testResourceName,
				Namespace: "default",
			},
			Spec: imagev1alpha1.NodeImageSpec{
				Name:     testImageName,
				Provider: testProvider,
			},
		}
		Expect(k8sClient.Create(ctx, nodeImage)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, nodeImage)
		})

		By("seeding status.releases (normally populated by the release controller)")
		nodeImage.Status.Releases = []string{"vsphere-1.2.3"}
		nodeImage.Status.State = imagev1alpha1.NodeImagePending
		Expect(k8sClient.Status().Update(ctx, nodeImage)).To(Succeed())

		By("reconciling the NodeImage")
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		Expect(err).NotTo(HaveOccurred())

		By("observing the NodeImage reported as Available")
		reconciled := &imagev1alpha1.NodeImage{}
		Expect(k8sClient.Get(ctx, namespacedName, reconciled)).To(Succeed())
		Expect(reconciled.Status.State).To(Equal(imagev1alpha1.NodeImageAvailable))

		By("confirming the image was imported into vSphere as a template")
		finder := vsphereFinder()
		vm, err := finder.VirtualMachine(ctx, testutil.VCSimFolder+"/"+testImageName)
		Expect(err).NotTo(HaveOccurred())

		var managedVM mo.VirtualMachine
		Expect(vm.Properties(ctx, vm.Reference(), []string{"config.template"}, &managedVM)).To(Succeed())
		Expect(managedVM.Config).NotTo(BeNil())
		Expect(managedVM.Config.Template).To(BeTrue())
	})

	It("destroys the vSphere template when the NodeImage is deleted", func() {
		namespacedName := types.NamespacedName{Name: testDeleteResourceName, Namespace: "default"}
		templatePath := testutil.VCSimFolder + "/" + testDeleteImageName

		By("creating and importing the NodeImage")
		nodeImage := &imagev1alpha1.NodeImage{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testDeleteResourceName,
				Namespace: "default",
			},
			Spec: imagev1alpha1.NodeImageSpec{
				Name:     testDeleteImageName,
				Provider: testProvider,
			},
		}
		Expect(k8sClient.Create(ctx, nodeImage)).To(Succeed())

		nodeImage.Status.Releases = []string{"vsphere-1.2.3"}
		nodeImage.Status.State = imagev1alpha1.NodeImagePending
		Expect(k8sClient.Status().Update(ctx, nodeImage)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		Expect(err).NotTo(HaveOccurred())

		By("confirming the template exists before deletion")
		finder := vsphereFinder()
		_, err = finder.VirtualMachine(ctx, templatePath)
		Expect(err).NotTo(HaveOccurred())

		By("deleting the NodeImage")
		// The finalizer added during the create reconcile keeps the object alive
		// (deletion timestamp set) until the deletion reconcile runs.
		Expect(k8sClient.Delete(ctx, nodeImage)).To(Succeed())

		By("reconciling the deletion")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		Expect(err).NotTo(HaveOccurred())

		By("confirming the template was destroyed in vSphere")
		_, err = finder.VirtualMachine(ctx, templatePath)
		Expect(err).To(HaveOccurred())

		By("confirming the NodeImage was removed once the finalizer cleared")
		err = k8sClient.Get(ctx, namespacedName, &imagev1alpha1.NodeImage{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})
})

// vsphereFinder returns a govmomi finder scoped to the simulator's datacenter,
// used to assert on inventory state independently of the code under test.
func vsphereFinder() *find.Finder {
	gclient, err := vcsim.Client(ctx)
	Expect(err).NotTo(HaveOccurred())

	finder := find.NewFinder(gclient.Client, true)
	dc, err := finder.DatacenterOrDefault(ctx, testutil.VCSimDatacenter)
	Expect(err).NotTo(HaveOccurred())
	finder.SetDatacenter(dc)

	return finder
}
