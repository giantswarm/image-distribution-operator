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
		gclient, err := vcsim.Client(ctx)
		Expect(err).NotTo(HaveOccurred())

		finder := find.NewFinder(gclient.Client, true)
		dc, err := finder.DatacenterOrDefault(ctx, testutil.VCSimDatacenter)
		Expect(err).NotTo(HaveOccurred())
		finder.SetDatacenter(dc)

		vm, err := finder.VirtualMachine(ctx, testutil.VCSimFolder+"/"+testImageName)
		Expect(err).NotTo(HaveOccurred())

		var managedVM mo.VirtualMachine
		Expect(vm.Properties(ctx, vm.Reference(), []string{"config.template"}, &managedVM)).To(Succeed())
		Expect(managedVM.Config).NotTo(BeNil())
		Expect(managedVM.Config.Template).To(BeTrue())
	})
})
