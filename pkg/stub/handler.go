package stub

import (
	"context"
	"fmt"
	"reflect"

	v1alpha1 "github.com/dougbtv/asterisk-operator/pkg/apis/voip/v1alpha1"

	"github.com/operator-framework/operator-sdk/pkg/sdk"
	"github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// NewHandler used here.
func NewHandler() sdk.Handler {
	return &Handler{}
}

// Handler used elsewhere.
type Handler struct {
}

// Handle method.
func (h *Handler) Handle(ctx context.Context, event sdk.Event) error {
	switch o := event.Object.(type) {
	case *v1alpha1.Asterisk:
		asterisk := o

		// Ignore the delete event since the garbage collector will clean up all secondary resources for the CR
		// All secondary resources must have the CR set as their OwnerReference for this to be the case
		if event.Deleted {
			return nil
		}

		// Create the deployment if it doesn't exist
		dep := deploymentForAsterisk(asterisk)
		err := sdk.Create(dep)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create deployment: %v", err)
		}

		// Ensure the deployment size is the same as the spec
		err = sdk.Get(dep)
		if err != nil {
			return fmt.Errorf("failed to get deployment: %v", err)
		}
		// config := asterisk.Spec.Config
		// logrus.Infof("The config: %v", config)
		size := asterisk.Spec.Size
		if *dep.Spec.Replicas != size {
			dep.Spec.Replicas = &size
			err = sdk.Update(dep)
			if err != nil {
				return fmt.Errorf("failed to update deployment: %v", err)
			}
		}

		// Update the Asterisk status with the pod names
		podList := podList()
		labelSelector := labels.SelectorFromSet(labelsForAsterisk(asterisk.Name)).String()
		listOps := &metav1.ListOptions{LabelSelector: labelSelector}
		err = sdk.List(asterisk.Namespace, podList, sdk.WithListOptions(listOps))
		if err != nil {
			return fmt.Errorf("failed to list pods: %v", err)
		}
		podNames := getPodNames(podList.Items)
		if !reflect.DeepEqual(podNames, asterisk.Status.Nodes) {
			asterisk.Status.Nodes = podNames
			logrus.Infof("!bang Pod names: %v", podNames)
			err := sdk.Update(asterisk)
			if err != nil {
				return fmt.Errorf("failed to update asterisk status: %v", err)
			}
		}
	}
	return nil
}

// deploymentForAsterisk returns a asterisk Deployment object
func deploymentForAsterisk(m *v1alpha1.Asterisk) *appsv1.Deployment {
	ls := labelsForAsterisk(m.Name)
	replicas := m.Spec.Size

	dep := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: ls,
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: ls,
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{{
						Image: "dougbtv/asterisk14",
						Name:  "asterisk",
						// Command: []string{"/bin/bash", "-c", "cat /etc/asterisk/entrypoint.sh | /bin/bash"},
					}},
				},
			},
		},
	}
	addOwnerRefToObject(dep, asOwner(m))
	return dep
}

// labelsForAsterisk returns the labels for selecting the resources
// belonging to the given asterisk CR name.
func labelsForAsterisk(name string) map[string]string {
	return map[string]string{"app": "asterisk", "asterisk_cr": name}
}

// addOwnerRefToObject appends the desired OwnerReference to the object
func addOwnerRefToObject(obj metav1.Object, ownerRef metav1.OwnerReference) {
	obj.SetOwnerReferences(append(obj.GetOwnerReferences(), ownerRef))
}

// asOwner returns an OwnerReference set as the asterisk CR
func asOwner(m *v1alpha1.Asterisk) metav1.OwnerReference {
	trueVar := true
	return metav1.OwnerReference{
		APIVersion: m.APIVersion,
		Kind:       m.Kind,
		Name:       m.Name,
		UID:        m.UID,
		Controller: &trueVar,
	}
}

// podList returns a v1.PodList object
func podList() *v1.PodList {
	return &v1.PodList{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
	}
}

// getPodNames returns the pod names of the array of pods passed in
func getPodNames(pods []v1.Pod) []string {
	var podNames []string
	for _, pod := range pods {
		logrus.Infof("!bang pod everything: %v", pod)
		podNames = append(podNames, pod.Name)
	}
	return podNames
}
