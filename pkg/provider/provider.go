package provider

import "context"

// Provider defines the interface for image distribution providers
type Provider interface {
	// Exists checks if an image already exists in the provider's catalog
	// name: the image name
	// loc: the location identifier within the provider
	Exists(ctx context.Context, name string, loc string) (bool, error)

	// Create imports and uploads an image to the provider's catalog
	// imageURL: the URL where the image can be downloaded from
	// imageName: the name to give the image in the catalog
	// loc: the location identifier within the provider
	Create(ctx context.Context, imageURL string, imageName string, loc string) error

	// Delete removes an image from the provider's catalog
	// name: the image name to delete
	// loc: the location identifier within the provider
	Delete(ctx context.Context, name string, loc string) error

	// GetLocations returns a map of all configured locations for this provider
	GetLocations() map[string]interface{}
}
