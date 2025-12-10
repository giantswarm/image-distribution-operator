package image

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	images "github.com/giantswarm/image-distribution-operator/api/image/v1alpha1"
	releases "github.com/giantswarm/release-operator/v4/api/v1alpha1"
)

func TestGetImageProvider(t *testing.T) {
	testCases := []struct {
		name             string
		releaseName      string
		expectedProvider string
		expectError      bool
	}{
		{
			name:             "case 0: vsphere release extracts vsphere provider",
			releaseName:      "vsphere-1.2.3",
			expectedProvider: "vsphere",
			expectError:      false,
		},
		{
			name:             "case 1: cloud-director release extracts cloud-director provider",
			releaseName:      "cloud-director-1.2.3",
			expectedProvider: "cloud-director",
			expectError:      false,
		},
		{
			name:             "case 2: vsphere release with patch version",
			releaseName:      "vsphere-20.0.0",
			expectedProvider: "vsphere",
			expectError:      false,
		},
		{
			name:             "case 3: cloud-director release with patch version",
			releaseName:      "cloud-director-0.10.5",
			expectedProvider: "cloud-director",
			expectError:      false,
		},
		{
			name:        "case 4: invalid release name without version",
			releaseName: "vsphere",
			expectError: true,
		},
		{
			name:        "case 5: invalid release name with invalid format",
			releaseName: "invalid-release-name",
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := GetImageProvider(tc.releaseName)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedProvider, provider)
			}
		})
	}
}

func TestGetProviderFromProviderName(t *testing.T) {
	testCases := []struct {
		name             string
		providerName     string
		expectedProvider string
	}{
		{
			name:             "case 0: vsphere maps to capv",
			providerName:     "vsphere",
			expectedProvider: "capv",
		},
		{
			name:             "case 1: cloud-director maps to capvcd",
			providerName:     "cloud-director",
			expectedProvider: "capvcd",
		},
		{
			name:             "case 2: unknown provider returns same name",
			providerName:     "aws",
			expectedProvider: "aws",
		},
		{
			name:             "case 3: test provider returns same name",
			providerName:     "test",
			expectedProvider: "test",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			provider := getProviderFromProviderName(tc.providerName)
			assert.Equal(t, tc.expectedProvider, provider)
		})
	}
}

func TestGetNodeImageFromRelease(t *testing.T) {
	testCases := []struct {
		name               string
		release            *releases.Release
		flatcarChannel     string
		expectedImageName  string
		expectedProvider   string
		expectedObjectName string
		expectError        bool
	}{
		{
			name: "case 0: vsphere release creates correct node image",
			release: &releases.Release{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vsphere-1.2.3",
				},
				Spec: releases.ReleaseSpec{
					Components: []releases.ReleaseSpecComponent{
						{Name: "flatcar", Version: "3975.2.0"},
						{Name: "kubernetes", Version: "v1.30.4"},
						{Name: "os-tooling", Version: "v1.18.1"},
					},
				},
			},
			flatcarChannel:     "stable",
			expectedImageName:  "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs",
			expectedProvider:   "capv",
			expectedObjectName: "capv-flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs",
			expectError:        false,
		},
		{
			name: "case 1: cloud-director release creates correct node image",
			release: &releases.Release{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cloud-director-0.10.5",
				},
				Spec: releases.ReleaseSpec{
					Components: []releases.ReleaseSpecComponent{
						{Name: "flatcar", Version: "3975.2.0"},
						{Name: "kubernetes", Version: "v1.30.4"},
						{Name: "os-tooling", Version: "v1.18.1"},
					},
				},
			},
			flatcarChannel:     "stable",
			expectedImageName:  "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs",
			expectedProvider:   "capvcd",
			expectedObjectName: "capvcd-flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs",
			expectError:        false,
		},
		{
			name: "case 2: missing flatcar component returns error",
			release: &releases.Release{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vsphere-1.2.3",
				},
				Spec: releases.ReleaseSpec{
					Components: []releases.ReleaseSpecComponent{
						{Name: "kubernetes", Version: "v1.30.4"},
						{Name: "os-tooling", Version: "v1.18.1"},
					},
				},
			},
			flatcarChannel: "stable",
			expectError:    true,
		},
		{
			name: "case 3: missing kubernetes component returns error",
			release: &releases.Release{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cloud-director-0.10.5",
				},
				Spec: releases.ReleaseSpec{
					Components: []releases.ReleaseSpecComponent{
						{Name: "flatcar", Version: "3975.2.0"},
						{Name: "os-tooling", Version: "v1.18.1"},
					},
				},
			},
			flatcarChannel: "stable",
			expectError:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			nodeImage, err := GetNodeImageFromRelease(tc.release, tc.flatcarChannel)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedImageName, nodeImage.Spec.Name)
				assert.Equal(t, tc.expectedProvider, nodeImage.Spec.Provider)
				assert.Equal(t, tc.expectedObjectName, nodeImage.Name)
			}
		})
	}
}

func TestGetImageKey(t *testing.T) {
	testCases := []struct {
		name             string
		nodeImage        *images.NodeImage
		expectedImageKey string
	}{
		{
			name: "case 0: vsphere node image generates correct S3 key",
			nodeImage: &images.NodeImage{
				Spec: images.NodeImageSpec{
					Name:     "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs",
					Provider: "capv",
				},
			},
			expectedImageKey: "capv/flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs/flatcar-stable-3975.2.0-kube-v1.30.4.ova",
		},
		{
			name: "case 1: cloud-director node image generates correct S3 key",
			nodeImage: &images.NodeImage{
				Spec: images.NodeImageSpec{
					Name:     "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs",
					Provider: "capvcd",
				},
			},
			expectedImageKey: "capv/flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs/flatcar-stable-3975.2.0-kube-v1.30.4.ova",
		},
		{
			name: "case 2: image with different kubernetes version",
			nodeImage: &images.NodeImage{
				Spec: images.NodeImageSpec{
					Name:     "flatcar-stable-3975.2.0-kube-1.29.0-tooling-1.18.1-gs",
					Provider: "capv",
				},
			},
			expectedImageKey: "capv/flatcar-stable-3975.2.0-kube-1.29.0-tooling-1.18.1-gs/flatcar-stable-3975.2.0-kube-v1.29.0.ova",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			imageKey := GetImageKey(tc.nodeImage)
			assert.Equal(t, tc.expectedImageKey, imageKey)
		})
	}
}

func TestBuildImageName(t *testing.T) {
	testCases := []struct {
		name              string
		flatcarChannel    string
		flatcarVersion    string
		kubernetesVersion string
		toolingVersion    string
		expectedName      string
	}{
		{
			name:              "case 0: build image name with v prefixes",
			flatcarChannel:    "stable",
			flatcarVersion:    "3975.2.0",
			kubernetesVersion: "v1.30.4",
			toolingVersion:    "v1.18.1",
			expectedName:      "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs",
		},
		{
			name:              "case 1: build image name without v prefixes",
			flatcarChannel:    "stable",
			flatcarVersion:    "3975.2.0",
			kubernetesVersion: "1.30.4",
			toolingVersion:    "1.18.1",
			expectedName:      "flatcar-stable-3975.2.0-kube-1.30.4-tooling-1.18.1-gs",
		},
		{
			name:              "case 2: build image name with beta channel",
			flatcarChannel:    "beta",
			flatcarVersion:    "3975.2.0",
			kubernetesVersion: "v1.30.4",
			toolingVersion:    "v1.18.1",
			expectedName:      "flatcar-beta-3975.2.0-kube-1.30.4-tooling-1.18.1-gs",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			imageName := buildImageName(tc.flatcarChannel, tc.flatcarVersion, tc.kubernetesVersion, tc.toolingVersion)
			assert.Equal(t, tc.expectedName, imageName)
		})
	}
}
