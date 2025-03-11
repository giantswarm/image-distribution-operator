package image

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	images "github.com/giantswarm/image-distribution-operator/api/image/v1alpha1"
)

// Config is a struct that holds the configuration for the Client
type Config struct {
	Client    client.Client
	Namespace string
	Log       logr.Logger
	Release   string
}

// Client holds the client and the namespace for node image objects
type Client struct {
	client.Client
	log       logr.Logger
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
		log:       c.Log,
		Namespace: c.Namespace,
		Release:   c.Release,
	}

	return client, nil
}

func (i *Client) RemoveImage(ctx context.Context, image string) error {
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
	// If there are still releases in the list, update the object
	if len(object.Status.Releases) > 0 {
		i.log.Info(fmt.Sprintf("Removing release %s from status of node image %s", i.Release, object.Name))
		return i.Client.Update(ctx, object)
	}

	// If there are no releases left, delete the object
	i.log.Info(fmt.Sprintf("Deleting node image %s", object.Name))
	return i.Client.Delete(ctx, object)
}

func (i *Client) CreateOrUpdateImage(ctx context.Context, image *images.NodeImage) error {
	// Get Image Object
	object := &images.NodeImage{}
	if err := i.Client.Get(ctx, client.ObjectKey{
		Namespace: i.Namespace,
		Name:      image.Name,
	}, object); err != nil {
		if apierrors.IsNotFound(err) {
			// Create the Image if it does not exist yet
			i.log.Info(fmt.Sprintf("Creating node image %s", object.Name))
			image.Namespace = i.Namespace
			return i.Create(ctx, image)
		}
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

	i.log.Info(fmt.Sprintf("Adding release %s to the status of node image %s", i.Release, object.Name))
	return i.Client.Update(ctx, object)
}
