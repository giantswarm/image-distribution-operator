package imagelist

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Config is a struct that holds the configuration for the ImageList object
type Config struct {
	Client        client.Client
	ListName      string
	ListNamespace string
	Log           logr.Logger
}

// ImageList is a struct that holds a list of images
type ImageList struct {
	client.Client
	log           logr.Logger
	ListName      string
	ListNamespace string
	Images        map[string]string
}

// New creates a new ImageList object for a configmap with the list of images
func New(c Config, ctx context.Context) (*ImageList, error) {
	// create a new ImageList object
	list := &ImageList{
		Client:        c.Client,
		log:           c.Log,
		ListName:      c.ListName,
		ListNamespace: c.ListNamespace,
		Images:        map[string]string{},
	}

	// sync the list of images with the configmap
	if err := list.updateImageList(ctx); err != nil {
		return nil, err
	}
	return list, nil
}

func (i *ImageList) RemoveImage(ctx context.Context, image string) error {
	delete(i.Images, image)
	return i.updateConfigmap(ctx)
}

func (i *ImageList) AddImage(ctx context.Context, image string) error {
	if i.Images[image] != "" {
		return nil
	}
	i.Images[image] = ""
	return i.updateConfigmap(ctx)
}

// UpdateConfigmap updates the configmap with the list of images
func (i *ImageList) updateConfigmap(ctx context.Context) error {
	// get cm with list of images
	object := &corev1.ConfigMap{}
	if err := i.Client.Get(ctx, client.ObjectKey{
		Namespace: i.ListNamespace,
		Name:      i.ListName,
	}, object); err != nil {
		return err
	}

	// update the list of images inside the configmap
	object.Data = i.Images
	return i.Client.Update(ctx, object)
}

func (i *ImageList) updateImageList(ctx context.Context) error {
	// get cm with list of images
	object := &corev1.ConfigMap{}
	if err := i.Client.Get(ctx, client.ObjectKey{
		Namespace: i.ListNamespace,
		Name:      i.ListName,
	}, object); err != nil {
		// if the configmap does not exist, create it
		if apierrors.IsNotFound(err) {
			object = getEmptyImageListConfigMap(i.ListName, i.ListNamespace)
			if err := i.Client.Create(ctx, object); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	// update the list of images inside the ImageList object
	i.Images = object.Data
	return nil
}

func getEmptyImageListConfigMap(name string, namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"managed-by": "image-distribution-operator",
			},
		},
		Data: map[string]string{},
	}
}
