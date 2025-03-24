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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// NodeImageSpec defines the desired state of NodeImage.
type NodeImageSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Name is the name of the image
	Name string `json:"name"`
	// Provider is the provider that the image is going to be used for
	Provider string `json:"provider"`
}

// NodeImageState is the state of the image
type NodeImageState string

const (
	NodeImagePending   NodeImageState = "Pending"
	NodeImageUploading NodeImageState = "Uploading"
	NodeImageAvailable NodeImageState = "Available"
	NodeImageError     NodeImageState = "Error"
	NodeImageDeleting  NodeImageState = "Deleting"
	NodeImageDeleted   NodeImageState = "Deleted"
)

// NodeImageStatus defines the observed state of NodeImage.
type NodeImageStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// Releases is the list of releases that the image is used in
	Releases []string `json:"releases"`

	// State is the state that the image is currently in
	State NodeImageState `json:"state"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// NodeImage is the Schema for the nodeimages API.
type NodeImage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeImageSpec   `json:"spec,omitempty"`
	Status NodeImageStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeImageList contains a list of NodeImage.
type NodeImageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeImage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NodeImage{}, &NodeImageList{})
}
