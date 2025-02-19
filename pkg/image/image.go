package image

import (
	"fmt"
	"strings"

	"github.com/giantswarm/release-operator/v4/api/v1alpha1"
)

func GetImageName(release *v1alpha1.Release, flatcarChannel string) (string, error) {

	var flatcarVersion, kubernetesVersion, toolingVersion string
	{
		flatcar, err := getReleaseComponent(release, "flatcar")
		if err != nil {
			return "", err
		}
		flatcarVersion = flatcar.Version

		kubernetes, err := getReleaseComponent(release, "kubernetes")
		if err != nil {
			return "", err
		}
		kubernetesVersion = kubernetes.Version

		tooling, err := getReleaseComponent(release, "os-tooling")
		if err != nil {
			return "", err
		}
		toolingVersion = tooling.Version
	}

	if flatcarVersion == "" {
		return "", fmt.Errorf("flatcar version is empty")
	}
	if kubernetesVersion == "" {
		return "", fmt.Errorf("kubernetes version is empty")
	}
	if toolingVersion == "" {
		return "", fmt.Errorf("tooling version is empty")
	}
	if flatcarChannel == "" {
		return "", fmt.Errorf("flatcar channel is empty")
	}

	return buildImageName(flatcarChannel, flatcarVersion, kubernetesVersion, toolingVersion), nil
}

// taken from github.com/giantswarm/capi-image-builder
func buildImageName(flatcarChannel, flatcarVersion, kubernetesVersion, toolingVersion string) string {
	return fmt.Sprintf(
		"flatcar-%s-%s-kube-%s-tooling-%s-gs",
		flatcarChannel,
		flatcarVersion,
		strings.TrimPrefix(kubernetesVersion, "v"),
		strings.TrimPrefix(toolingVersion, "v"),
	)
}

func getReleaseComponent(release *v1alpha1.Release, component string) (v1alpha1.ReleaseSpecComponent, error) {
	components := release.Spec.Components

	for _, c := range components {
		if c.Name == component {
			return c, nil
		}
	}

	return v1alpha1.ReleaseSpecComponent{}, fmt.Errorf("component %s not found in release %s", component, release.Name)
}
