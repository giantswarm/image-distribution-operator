package image

import (
	"fmt"
	"regexp"
	"strings"

	images "github.com/giantswarm/image-distribution-operator/api/image/v1alpha1"

	releases "github.com/giantswarm/release-operator/v4/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func GetNodeImageFromRelease(release *releases.Release, flatcarChannel string) (*images.NodeImage, error) {
	imageName, err := getImageName(release, flatcarChannel)
	if err != nil {
		return &images.NodeImage{}, err
	}

	providerName, err := getImageProvider(release.Name)
	if err != nil {
		return &images.NodeImage{}, err
	}

	return GetNodeImage(imageName, providerName, release.Name), nil
}

func GetNodeImage(imageName, providerName, releaseName string) *images.NodeImage {
	return &images.NodeImage{
		ObjectMeta: metav1.ObjectMeta{
			Name: strings.Join([]string{providerName, imageName}, "-"),
		},

		Spec: images.NodeImageSpec{
			Name:     imageName,
			Provider: providerName,
		},
	}
}

func getImageName(release *releases.Release, flatcarChannel string) (string, error) {

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

func getImageProvider(release string) (string, error) {
	// the provider name is the first part of the name before the first digit
	regexp := regexp.MustCompile(`^([a-z-]+)-\d+\.\d+\.\d+`)
	matches := regexp.FindStringSubmatch(release)
	if len(matches) > 1 {
		return matches[1], nil
	}
	return "", fmt.Errorf("provider name not found in release %s", release)
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

func getReleaseComponent(release *releases.Release, component string) (releases.ReleaseSpecComponent, error) {
	components := release.Spec.Components

	for _, c := range components {
		if c.Name == component {
			return c, nil
		}
	}

	return releases.ReleaseSpecComponent{}, fmt.Errorf("component %s not found in release %s", component, release.Name)
}
