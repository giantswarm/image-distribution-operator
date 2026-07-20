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

package nodeimageproxmox

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	imagev1alpha1 "github.com/giantswarm/image-distribution-operator/api/image/v1alpha1"
)

var _ = Describe("NodeImage Proxmox reconciliation", func() {
	It("imports the S3 image into Proxmox as a template", func() {
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
		nodeImage.Status.Releases = []string{"proxmox-1.2.3"}
		nodeImage.Status.State = imagev1alpha1.NodeImagePending
		Expect(k8sClient.Status().Update(ctx, nodeImage)).To(Succeed())

		By("reconciling the NodeImage")
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		Expect(err).NotTo(HaveOccurred())

		By("observing the NodeImage reported as Available")
		reconciled := &imagev1alpha1.NodeImage{}
		Expect(k8sClient.Get(ctx, namespacedName, reconciled)).To(Succeed())
		Expect(reconciled.Status.State).To(Equal(imagev1alpha1.NodeImageAvailable))

		By("confirming the image was imported into Proxmox as a template")
		_, template, found := fakeProxmox.FindByName(testImageName)
		Expect(found).To(BeTrue())
		Expect(template).To(BeTrue())
	})

	It("destroys the Proxmox template when the NodeImage is deleted", func() {
		namespacedName := types.NamespacedName{Name: testDeleteResourceName, Namespace: "default"}

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

		nodeImage.Status.Releases = []string{"proxmox-1.2.3"}
		nodeImage.Status.State = imagev1alpha1.NodeImagePending
		Expect(k8sClient.Status().Update(ctx, nodeImage)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		Expect(err).NotTo(HaveOccurred())

		By("confirming the template exists before deletion")
		_, _, found := fakeProxmox.FindByName(testDeleteImageName)
		Expect(found).To(BeTrue())

		By("deleting the NodeImage")
		// The finalizer added during the create reconcile keeps the object alive
		// (deletion timestamp set) until the deletion reconcile runs.
		Expect(k8sClient.Delete(ctx, nodeImage)).To(Succeed())

		By("reconciling the deletion")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		Expect(err).NotTo(HaveOccurred())

		By("confirming the template was destroyed in Proxmox")
		_, _, found = fakeProxmox.FindByName(testDeleteImageName)
		Expect(found).To(BeFalse())

		By("confirming the NodeImage was removed once the finalizer cleared")
		err = k8sClient.Get(ctx, namespacedName, &imagev1alpha1.NodeImage{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	It("does not re-import when the template already exists", func() {
		namespacedName := types.NamespacedName{Name: testIdempotentResourceName, Namespace: "default"}

		By("creating and importing the NodeImage")
		nodeImage := &imagev1alpha1.NodeImage{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testIdempotentResourceName,
				Namespace: "default",
			},
			Spec: imagev1alpha1.NodeImageSpec{
				Name:     testIdempotentImageName,
				Provider: testProvider,
			},
		}
		Expect(k8sClient.Create(ctx, nodeImage)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, nodeImage)
		})

		nodeImage.Status.Releases = []string{"proxmox-1.2.3"}
		nodeImage.Status.State = imagev1alpha1.NodeImagePending
		Expect(k8sClient.Status().Update(ctx, nodeImage)).To(Succeed())

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		Expect(err).NotTo(HaveOccurred())

		By("recording the imported template's VMID")
		originalVMID, template, found := fakeProxmox.FindByName(testIdempotentImageName)
		Expect(found).To(BeTrue())
		Expect(template).To(BeTrue())

		By("reconciling a second time")
		_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		Expect(err).NotTo(HaveOccurred())

		By("confirming the template was not re-imported")
		// A re-import would delete and recreate the VM, yielding a new VMID; an
		// unchanged VMID proves Exists() short-circuited the import.
		vmid, template, found := fakeProxmox.FindByName(testIdempotentImageName)
		Expect(found).To(BeTrue())
		Expect(template).To(BeTrue())
		Expect(vmid).To(Equal(originalVMID))

		By("confirming the NodeImage is still Available")
		reconciled := &imagev1alpha1.NodeImage{}
		Expect(k8sClient.Get(ctx, namespacedName, reconciled)).To(Succeed())
		Expect(reconciled.Status.State).To(Equal(imagev1alpha1.NodeImageAvailable))
	})

	It("marks the NodeImage as Missing when the S3 object is absent", func() {
		namespacedName := types.NamespacedName{Name: testMissingResourceName, Namespace: "default"}

		By("creating the NodeImage for an unseeded image")
		nodeImage := &imagev1alpha1.NodeImage{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testMissingResourceName,
				Namespace: "default",
			},
			Spec: imagev1alpha1.NodeImageSpec{
				Name:     testMissingImageName,
				Provider: testProvider,
			},
		}
		Expect(k8sClient.Create(ctx, nodeImage)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, nodeImage)
		})

		nodeImage.Status.Releases = []string{"proxmox-1.2.3"}
		nodeImage.Status.State = imagev1alpha1.NodeImagePending
		Expect(k8sClient.Status().Update(ctx, nodeImage)).To(Succeed())

		By("reconciling the NodeImage")
		// The HEAD check against the fake bucket 404s, so the reconcile requeues
		// without error rather than propagating a failure.
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		Expect(err).NotTo(HaveOccurred())

		By("observing the NodeImage reported as Missing")
		reconciled := &imagev1alpha1.NodeImage{}
		Expect(k8sClient.Get(ctx, namespacedName, reconciled)).To(Succeed())
		Expect(reconciled.Status.State).To(Equal(imagev1alpha1.NodeImageMissing))

		By("confirming no template was imported into Proxmox")
		_, _, found := fakeProxmox.FindByName(testMissingImageName)
		Expect(found).To(BeFalse())
	})

	It("marks the NodeImage as Error when the qcow2 download fails", func() {
		namespacedName := types.NamespacedName{Name: testErrorResourceName, Namespace: "default"}

		By("creating the NodeImage for an image seeded with garbage bytes")
		nodeImage := &imagev1alpha1.NodeImage{
			ObjectMeta: metav1.ObjectMeta{
				Name:      testErrorResourceName,
				Namespace: "default",
			},
			Spec: imagev1alpha1.NodeImageSpec{
				Name:     testErrorImageName,
				Provider: testProvider,
			},
		}
		Expect(k8sClient.Create(ctx, nodeImage)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, nodeImage)
		})

		nodeImage.Status.Releases = []string{"proxmox-1.2.3"}
		nodeImage.Status.State = imagev1alpha1.NodeImagePending
		Expect(k8sClient.Status().Update(ctx, nodeImage)).To(Succeed())

		By("reconciling the NodeImage")
		// The object exists (HEAD 200) but is not a valid qcow2, so the server-side
		// download task fails and the reconcile propagates the error after setting
		// the status.
		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
		Expect(err).To(HaveOccurred())

		By("observing the NodeImage reported as Error")
		reconciled := &imagev1alpha1.NodeImage{}
		Expect(k8sClient.Get(ctx, namespacedName, reconciled)).To(Succeed())
		Expect(reconciled.Status.State).To(Equal(imagev1alpha1.NodeImageError))

		By("confirming no template was imported into Proxmox")
		_, _, found := fakeProxmox.FindByName(testErrorImageName)
		Expect(found).To(BeFalse())
	})
})
