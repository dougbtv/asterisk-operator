package v1alpha1

import (
  metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type AsteriskList struct {
  metav1.TypeMeta `json:",inline"`
  metav1.ListMeta `json:"metadata"`
  Items           []Asterisk `json:"items"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type Asterisk struct {
  metav1.TypeMeta   `json:",inline"`
  metav1.ObjectMeta `json:"metadata"`
  Spec              AsteriskSpec   `json:"spec"`
  Status            AsteriskStatus `json:"status,omitempty"`
}

type AsteriskSpec struct {
  // Size is the size of the Asterisk deployment
  Size   int32  `json:"size"`
  Config string `json:"config"`
}

type AsteriskStatus struct {
  // Nodes are the names of the Asterisk pods
  Nodes []string `json:"nodes"`
}
