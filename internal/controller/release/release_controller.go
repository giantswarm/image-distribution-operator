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
	"fmt"

	"github.com/giantswarm/image-distribution-operator/pkg/image"

	"github.com/giantswarm/release-operator/v4/api/v1alpha1"
	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	ReleaseControllerFinalizer = "image-distribution-operator.finalizers.giantswarm.io/release-controller"
)

// ReleaseReconciler reconciles a Release object
type ReleaseReconciler struct {
	client.Client
	Log       logr.Logger
	Scheme    *runtime.Scheme
	Namespace string
}

// +kubebuilder:rbac:groups=release.giantswarm.io.giantswarm.io,resources=releases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=release.giantswarm.io.giantswarm.io,resources=releases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=release.giantswarm.io.giantswarm.io,resources=releases/finalizers,verbs=update

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
	log := r.Log.WithValues("release", req.NamespacedName)

	// Fetch the Release
	release := &v1alpha1.Release{}
	if err := r.Get(ctx, req.NamespacedName, release); err != nil {
		if apierrors.IsNotFound(err) {
			// Object not found. Return
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return ctrl.Result{}, err
	}

	flatcarChannel := "stable" // TODO: ensure that this is what it is supposed to be or if it comes from somewhere else

	nodeImage, err := image.GetNodeImageFromRelease(release, flatcarChannel)
	if err != nil {
		return ctrl.Result{}, err
	}

	imageClient, err := image.New(image.Config{
		Client:    r.Client,
		Namespace: r.Namespace,
		Log:       log,
		Release:   release.Name,
	})
	if err != nil {
		return ctrl.Result{}, err
	}

	// Handle deleted release
	if IsDeleted(release) {
		log.Info(fmt.Sprintf("Release %s is marked for deletion.", release.Name))
		if err := imageClient.RemoveImage(ctx, nodeImage.Name); err != nil {
			return ctrl.Result{}, err
		}

		// remove finalizer
		if controllerutil.ContainsFinalizer(release, ReleaseControllerFinalizer) {
			controllerutil.RemoveFinalizer(release, ReleaseControllerFinalizer)
			if err := r.Update(ctx, release); err != nil {
				return ctrl.Result{}, err
			}
			log.Info("Removed finalizer from release instance.")
		}
		return ctrl.Result{}, nil
	}

	// add finalizer
	if !controllerutil.ContainsFinalizer(release, ReleaseControllerFinalizer) {
		controllerutil.AddFinalizer(release, ReleaseControllerFinalizer)
		if err := r.Update(ctx, release); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Added finalizer to release instance.")
	}

	// Handle normal release
	if err := imageClient.CreateOrUpdateImage(ctx, nodeImage); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
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
