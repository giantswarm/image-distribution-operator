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

package image

import (
	"context"
	"fmt"

	imagev1alpha1 "github.com/giantswarm/image-distribution-operator/api/image/v1alpha1"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	NodeImageFinalizer = "image-distribution-operator.finalizers.giantswarm.io/node-image-controller"
)

// NodeImageReconciler reconciles a NodeImage object
type NodeImageReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=image.giantswarm.io,resources=nodeimages,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=image.giantswarm.io,resources=nodeimages/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=image.giantswarm.io,resources=nodeimages/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the NodeImage object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.20.0/pkg/reconcile
func (r *NodeImageReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("nodeImage", req.NamespacedName)

	// Fetch the NodeImage instance
	nodeImage := &imagev1alpha1.NodeImage{}
	err := r.Get(ctx, req.NamespacedName, nodeImage)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// TODO create client(s)

	// Handle deletion
	if IsDeleted(nodeImage) {
		log.Info(fmt.Sprintf("NodeImage %s is marked for deletion", nodeImage.Name))
		// TODO handle deletion

		// Remove finalizer
		if controllerutil.ContainsFinalizer(nodeImage, NodeImageFinalizer) {
			controllerutil.RemoveFinalizer(nodeImage, NodeImageFinalizer)
			if err := r.Update(ctx, nodeImage); err != nil {
				return ctrl.Result{}, err
			}
			log.Info(fmt.Sprintf("Finalizer %s removed from NodeImage %s", NodeImageFinalizer, nodeImage.Name))
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(nodeImage, NodeImageFinalizer) {
		controllerutil.AddFinalizer(nodeImage, NodeImageFinalizer)
		if err := r.Update(ctx, nodeImage); err != nil {
			return ctrl.Result{}, err
		}
		log.Info(fmt.Sprintf("Finalizer %s added to NodeImage %s", NodeImageFinalizer, nodeImage.Name))
	}

	// TODO handle create/update

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeImageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&imagev1alpha1.NodeImage{}).
		Named("image-nodeimage").
		Complete(r)
}

func IsDeleted(nodeImage *imagev1alpha1.NodeImage) bool {
	return !nodeImage.DeletionTimestamp.IsZero()
}
