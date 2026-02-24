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
	"net/http"
	"time"

	imagev1alpha1 "github.com/giantswarm/image-distribution-operator/api/image/v1alpha1"
	"github.com/giantswarm/image-distribution-operator/pkg/image"
	"github.com/giantswarm/image-distribution-operator/pkg/provider"
	"github.com/giantswarm/image-distribution-operator/pkg/s3"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	NodeImageFinalizer = "image-distribution-operator.finalizers.giantswarm.io/node-image-controller"
)

// NodeImageReconciler reconciles a NodeImage object
type NodeImageReconciler struct {
	client.Client
	S3Client             *s3.Client
	Providers            map[string]provider.Provider
	ImageRetentionPeriod time.Duration
}

// +kubebuilder:rbac:groups=image.giantswarm.io,resources=nodeimages,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=image.giantswarm.io,resources=nodeimages/status,verbs=get;update;patch

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
	log := log.FromContext(ctx)

	// Fetch the NodeImage instance
	nodeImage := &imagev1alpha1.NodeImage{}
	err := r.Get(ctx, req.NamespacedName, nodeImage)
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion
	if IsDeleted(nodeImage) {
		log.Info("NodeImage is being deleted", "nodeImage", nodeImage.Name)

		// Get the provider for this NodeImage
		prov, ok := r.Providers[nodeImage.Spec.Provider]
		if !ok {
			log.Info("Provider not configured - skipping deletion", "provider", nodeImage.Spec.Provider)
			// Remove finalizer even if provider is not configured
			if controllerutil.ContainsFinalizer(nodeImage, NodeImageFinalizer) {
				controllerutil.RemoveFinalizer(nodeImage, NodeImageFinalizer)
				if err := r.Update(ctx, nodeImage); err != nil {
					return ctrl.Result{}, err
				}
				log.Info("Finalizer removed from NodeImage", "finalizer", NodeImageFinalizer, "nodeImage", nodeImage.Name)
			}
			return ctrl.Result{}, nil
		}

		// Delete from all locations
		for loc := range prov.GetLocations() {
			if err := r.DeleteProvider(ctx, nodeImage, loc, prov); err != nil {
				if statusErr := r.UpdateStatus(ctx, nodeImage, imagev1alpha1.NodeImageError); statusErr != nil {
					return ctrl.Result{}, fmt.Errorf("failed to delete node image: %w\nfailed to update status: %w", err, statusErr)
				}
				return ctrl.Result{}, err
			}
		}

		// Remove finalizer
		if controllerutil.ContainsFinalizer(nodeImage, NodeImageFinalizer) {
			controllerutil.RemoveFinalizer(nodeImage, NodeImageFinalizer)
			if err := r.Update(ctx, nodeImage); err != nil {
				return ctrl.Result{}, err
			}
			log.Info("Finalizer removed from NodeImage", "finalizer", NodeImageFinalizer, "nodeImage", nodeImage.Name)
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer
	if !controllerutil.ContainsFinalizer(nodeImage, NodeImageFinalizer) {
		controllerutil.AddFinalizer(nodeImage, NodeImageFinalizer)
		if err := r.Update(ctx, nodeImage); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Finalizer added to NodeImage", "finalizer", NodeImageFinalizer, "nodeImage", nodeImage.Name)
	}

	// Check if the image is awaiting deletion
	if nodeImage.Status.State == imagev1alpha1.NodeImageAwaitingDeletion {
		if lastUsedStr, ok := nodeImage.Annotations[image.LastUsedAnnotation]; ok {
			lastUsedTime, err := time.Parse(time.RFC3339, lastUsedStr)
			if err == nil {
				expirationTime := lastUsedTime.Add(r.ImageRetentionPeriod)
				if time.Now().After(expirationTime) {
					log.Info("Image retention period expired - deleting NodeImage", "nodeImage", nodeImage.Name)
					if err := r.Delete(ctx, nodeImage); err != nil {
						return ctrl.Result{}, err
					}
					return ctrl.Result{}, nil
				}

				// Requeue after expiration
				requeueAfter := time.Until(expirationTime)
				log.Info("Image awaiting deletion", "nodeImage", nodeImage.Name, "requeueAfter", requeueAfter)
				return ctrl.Result{RequeueAfter: requeueAfter}, nil
			}
		}
	}

	// If no releases reference this image, initiate deletion.
	// Guard with state != "" to avoid acting on brand-new objects before the
	// release controller has had a chance to register the first release.
	if len(nodeImage.Status.Releases) == 0 && nodeImage.Status.State != "" {
		if r.ImageRetentionPeriod > 0 {
			if nodeImage.Annotations == nil {
				nodeImage.Annotations = make(map[string]string)
			}
			nodeImage.Annotations[image.LastUsedAnnotation] = time.Now().Format(time.RFC3339)
			if err := r.Update(ctx, nodeImage); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.UpdateStatus(ctx, nodeImage, imagev1alpha1.NodeImageAwaitingDeletion); err != nil {
				return ctrl.Result{}, err
			}
			log.Info("No releases reference this image - marking for deletion", "nodeImage", nodeImage.Name, "retentionPeriod", r.ImageRetentionPeriod)
			return ctrl.Result{RequeueAfter: r.ImageRetentionPeriod}, nil
		}
		log.Info("No releases reference this image - deleting", "nodeImage", nodeImage.Name)
		if err := r.Delete(ctx, nodeImage); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Get the URL of the image
	imageKey := image.GetImageKey(nodeImage)
	url := r.S3Client.GetURL(imageKey)

	// Check if the url is valid
	if err := r.S3Client.ValidURL(url); err != nil {
		log.Info("Invalid URL", "url", url)
		return ctrl.Result{}, fmt.Errorf("invalid URL: %s", url)
	}

	// Get the provider for this NodeImage
	prov, ok := r.Providers[nodeImage.Spec.Provider]
	if !ok {
		log.Info("Provider not configured - skipping NodeImage reconciliation", "provider", nodeImage.Spec.Provider, "nodeImage", nodeImage.Name)
		// Mark as error to indicate configuration issue
		// This gives users visibility that the provider needs to be configured
		if err := r.UpdateStatus(ctx, nodeImage, imagev1alpha1.NodeImageError); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update status for unconfigured provider: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// Process image for all locations in the provider
	for loc := range prov.GetLocations() {
		// check if the image is available
		if err := ImageAvailable(url); err != nil {
			log.Info("Image not available on S3 - marking as missing", "url", url, "response", err)
			if err := r.UpdateStatus(ctx, nodeImage, imagev1alpha1.NodeImageMissing); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to update status: %w", err)
			}
			return DefaultRequeue(), nil
		}
		if err := r.CreateProvider(ctx, nodeImage, url, loc, prov); err != nil {
			if statusErr := r.UpdateStatus(ctx, nodeImage, imagev1alpha1.NodeImageError); statusErr != nil {
				return ctrl.Result{}, fmt.Errorf("failed to create node image: %w\nfailed to update status: %w", err, statusErr)
			}
			return ctrl.Result{}, err
		}
	}

	return DefaultRequeue(), nil
}

func (r *NodeImageReconciler) CreateProvider(ctx context.Context, nodeImage *imagev1alpha1.NodeImage, url string, loc string, prov provider.Provider) error {
	log := log.FromContext(ctx)

	// check if the image is already uploaded
	if exists, err := prov.Exists(ctx, nodeImage.Spec.Name, loc); err != nil {
		return fmt.Errorf("failed to check if image exists: %w", err)
	} else if exists {
		// set the status
		return r.UpdateStatus(ctx, nodeImage, imagev1alpha1.NodeImageAvailable)
	}

	log.Info("Node image not found, uploading", "nodeImage", nodeImage.Name, "location", loc)

	// set the status
	if err := r.UpdateStatus(ctx, nodeImage, imagev1alpha1.NodeImageUploading); err != nil {
		return err
	}

	// import the image
	err := prov.Create(ctx, url, nodeImage.Spec.Name, loc)
	if err != nil {
		return fmt.Errorf("failed to import image: %w", err)
	}

	log.Info("Node image uploaded and processed", "nodeImage", nodeImage.Name, "location", loc)

	// set the status
	return r.UpdateStatus(ctx, nodeImage, imagev1alpha1.NodeImageAvailable)
}

func (r *NodeImageReconciler) DeleteProvider(ctx context.Context, nodeImage *imagev1alpha1.NodeImage, loc string, prov provider.Provider) error {
	log := log.FromContext(ctx)

	// set the status
	if err := r.UpdateStatus(ctx, nodeImage, imagev1alpha1.NodeImageDeleting); err != nil {
		return err
	}

	// delete the image
	if err := prov.Delete(ctx, nodeImage.Spec.Name, loc); err != nil {
		return fmt.Errorf("failed to delete image: %w", err)
	}

	log.Info("Node image deleted", "nodeImage", nodeImage.Name, "location", loc)

	// set the status
	return r.UpdateStatus(ctx, nodeImage, imagev1alpha1.NodeImageDeleted)
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeImageReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&imagev1alpha1.NodeImage{}).
		Named("image-nodeimage").
		Complete(r)
}

func (r *NodeImageReconciler) UpdateStatus(ctx context.Context, nodeImage *imagev1alpha1.NodeImage, state imagev1alpha1.NodeImageState) error {
	log := log.FromContext(ctx)
	if nodeImage.Status.State != state {
		nodeImage.Status.State = state
		if err := r.Status().Update(ctx, nodeImage); err != nil {
			return fmt.Errorf("failed to update status: %w", err)
		}
		log.Info("Node image status updated", "nodeImage", nodeImage.Name, "state", nodeImage.Status.State)
	}
	return nil
}

func IsDeleted(nodeImage *imagev1alpha1.NodeImage) bool {
	return !nodeImage.DeletionTimestamp.IsZero()
}

func ImageAvailable(url string) error {
	resp, err := http.Head(url) // #nosec G107
	if err != nil {
		return fmt.Errorf("error checking URL: %w", err)
	}

	// Ensure resp.Body is closed safely
	defer func() {
		if resp.Body != nil {
			if err := resp.Body.Close(); err != nil {
				fmt.Printf("Failed to close response body: %v", err)
			}
		}
	}()

	// HTTP 200-299 means the file exists
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	return fmt.Errorf("OVA file not found, status code: %d", resp.StatusCode)
}

func DefaultRequeue() reconcile.Result {
	return ctrl.Result{
		Requeue:      true,
		RequeueAfter: time.Minute * 5,
	}
}
