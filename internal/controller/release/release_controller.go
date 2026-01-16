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

package release

import (
	"context"
	"time"

	"github.com/giantswarm/image-distribution-operator/pkg/image"

	"github.com/giantswarm/releases/sdk/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	ReleaseControllerFinalizer = "image-distribution-operator.finalizers.giantswarm.io/release-controller"
)

// ReleaseReconciler reconciles a Release object
type ReleaseReconciler struct {
	client.Client
	Namespace string
	Providers map[string]interface{}
}

// +kubebuilder:rbac:groups=release.giantswarm.io,resources=releases,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=release.giantswarm.io,resources=releases/status,verbs=get;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Release object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *ReleaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the Release
	release := &v1alpha1.Release{}
	err := r.Get(ctx, req.NamespacedName, release)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	flatcarChannel := "stable" // TODO: ensure that this is what it is supposed to be or if it comes from somewhere else

	nodeImage, err := image.GetNodeImageFromRelease(release, flatcarChannel)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Check if the provider for this release is configured
	if _, ok := r.Providers[nodeImage.Spec.Provider]; !ok {
		log.Info("Provider not configured - skipping release", "provider", nodeImage.Spec.Provider, "release", release.Name)
		return ctrl.Result{}, nil
	}

	imageClient, err := image.New(image.Config{
		Client:    r.Client,
		Namespace: r.Namespace,
		Release:   release.Name,
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	// Handle deleted release
	if IsDeleted(release) {
		log.Info("Release is being deleted")

		// Remove release from image status
		if err := imageClient.RemoveReleaseFromNodeImageStatus(ctx, nodeImage.Name); err != nil {
			return ctrl.Result{}, err
		}

		// Handle deletion
		if err := imageClient.DeleteImage(ctx, nodeImage.Name); err != nil {
			return ctrl.Result{}, err
		}

		// remove finalizer
		if controllerutil.ContainsFinalizer(release, ReleaseControllerFinalizer) {
			controllerutil.RemoveFinalizer(release, ReleaseControllerFinalizer)
			if err := r.Update(ctx, release); err != nil {
				return ctrl.Result{}, err
			}
			log.Info("Finalizer removed from Release", "finalizer", ReleaseControllerFinalizer)
		}
		return ctrl.Result{}, nil
	}

	// add finalizer
	if !controllerutil.ContainsFinalizer(release, ReleaseControllerFinalizer) {
		controllerutil.AddFinalizer(release, ReleaseControllerFinalizer)
		if err := r.Update(ctx, release); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Finalizer added to Release", "finalizer", ReleaseControllerFinalizer)
	}

	// Handle creation
	if err := imageClient.CreateImage(ctx, nodeImage); err != nil {
		return ctrl.Result{}, err
	}

	// Add Releases to the image status
	if err := imageClient.AddReleaseToNodeImageStatus(ctx, nodeImage.Name); err != nil {
		return ctrl.Result{}, err
	}

	return DefaultRequeue(), nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ReleaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Release{}).
		Named("release").
		Complete(r)
}

// IsDeleted returns true if the release is marked for deletion.
func IsDeleted(release *v1alpha1.Release) bool {
	return !release.DeletionTimestamp.IsZero()
}

func DefaultRequeue() reconcile.Result {
	return ctrl.Result{
		Requeue:      true,
		RequeueAfter: time.Minute * 5,
	}
}
