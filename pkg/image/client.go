package image

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	images "github.com/giantswarm/image-distribution-operator/api/image/v1alpha1"
)

// Config is a struct that holds the configuration for the Client
type Config struct {
	Client    client.Client
	Namespace string
	Release   string
}

// Client holds the client and the namespace for node image objects
type Client struct {
	client.Client
	Namespace string
	Release   string
}

// New creates a new Client object
func New(c Config) (*Client, error) {
	if c.Namespace == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	if c.Client == nil {
		return nil, fmt.Errorf("client is required")
	}
	if c.Release == "" {
		return nil, fmt.Errorf("release is required")
	}

	// create a new ImageList object
	client := &Client{
		Client:    c.Client,
		Namespace: c.Namespace,
		Release:   c.Release,
	}

	return client, nil
}

func (i *Client) RemoveReleaseFromNodeImageStatus(ctx context.Context, image string) error {
	log := log.FromContext(ctx)

	// Get Image Object
	object := &images.NodeImage{}
	if err := i.Client.Get(ctx, client.ObjectKey{
		Namespace: i.Namespace,
		Name:      image,
	}, object); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// Check node image status and remove the release from the list
	for index, release := range object.Status.Releases {
		if release == i.Release {
			object.Status.Releases = append(object.Status.Releases[:index], object.Status.Releases[index+1:]...)
			break
		}
	}
	// Update the object
	log.Info("Removing release from the status of node image", "nodeImage", object.Name, "release", i.Release)
	return i.Client.Update(ctx, object)
}

func (i *Client) DeleteImage(ctx context.Context, image string) error {
	log := log.FromContext(ctx)

	// Get Image Object
	object := &images.NodeImage{}
	if err := i.Client.Get(ctx, client.ObjectKey{
		Namespace: i.Namespace,
		Name:      image,
	}, object); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// If there are still releases in the list, nothing to do
	if len(object.Status.Releases) > 0 {
		return nil
	}

	// If there are no releases left, delete the object
	log.Info("Deleting node image", "nodeImage", object.Name)
	return i.Client.Delete(ctx, object)
}

func (i *Client) CreateImage(ctx context.Context, image *images.NodeImage) error {
	log := log.FromContext(ctx)
    image.Namespace = i.Namespace
	err := i.Create(ctx, image)
	if apierrors.IsAlreadyExists(err) {
		return nil
	} else if err != nil {
		return err
	}
	log.Info("Created node image", "nodeImage", image.Name)
	return nil
}

func (i *Client) AddReleaseToNodeImageStatus(ctx context.Context, image string) error {
	log := log.FromContext(ctx)

	// Get Image Object
	object := &images.NodeImage{}
	if err := i.Client.Get(ctx, client.ObjectKey{
		Namespace: i.Namespace,
		Name:      image,
	}, object); err != nil {
		return err
	}

	// Check node image status
	for _, release := range object.Status.Releases {
		if release == i.Release {
			// release is already listed
			return nil
		}
	}
	// Add release to the list
	object.Status.Releases = append(object.Status.Releases, i.Release)

	log.Info("Adding release to the status of node image", "nodeImage", object.Name, "release", i.Release)
	return i.Client.Status().Update(ctx, object)
}
