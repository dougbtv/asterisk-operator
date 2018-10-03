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
	"time"

	"bytes"
	// "encoding/json"
	"io/ioutil"
	"net/http"
	"strings"
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

			// Here's where we ship off the work to create the SIP trunks.
			asterr := cycleAsteriskPods(podNames, podList, asterisk.Namespace, listOps)
			if asterr != nil {
				return asterr
			}

			err := sdk.Update(asterisk)
			if err != nil {
				return fmt.Errorf("failed to update asterisk status: %v", err)
			}
		}
	}
	return nil
}

// cycleAsteriskPod cycles each one of the asterisk pods, discovers the IP and then builds sip trunks for each one.
func cycleAsteriskPods(podNames []string, podList *v1.PodList, namespace string, listOps *metav1.ListOptions) error {

	// We have some weird race, let's try to sleep right away and then requery the api
	// time.Sleep(1500 * time.Millisecond)
	// _ = sdk.List(namespace, podList, sdk.WithListOptions(listOps))

	// We need to check if there's any blank IPs
	podIPs := getPodIPs(podList.Items)
	foundall := true

	maxtries := 40
	tries := 0
	numfound := 0

	for {

		// logrus.Infof("!bang Pod IPs: %v", podIPs)
		// logrus.Infof("!bang Lengths: %v < %v", len(podIPs), len(podNames))

		// If the list is too short, it's not found.
		if len(podIPs) < len(podNames) {
			foundall = false
		} else {
			// Cycle through all the pod IPs, look for blanks.
			for _, podip := range podIPs {
				if podip == "" {
					foundall = false
				} else {
					numfound++
				}
			}
		}

		if foundall {
			logrus.Infof("!bang TRACE -- FOUND ALL TRUE")
			break
		} else {
			// Sleep a little, then get the list again.
			time.Sleep(1500 * time.Millisecond)
			foundall = true
			numfound = 0
			logrus.Infof("Found only %v/%v IPs -- retrying (attempt %v/%v)", numfound, len(podNames), tries, maxtries)

			// Query the API again.
			err := sdk.List(namespace, podList, sdk.WithListOptions(listOps))
			podIPs = getPodIPs(podList.Items)
			// We might not care if this fails...
			if err != nil {
				return fmt.Errorf("failed to list pods during IP discovery: %v", err)
			}
		}

		tries++
		if tries >= maxtries {
			return fmt.Errorf("During IP discovery, exceeded %v", maxtries)
		}
	}

	// No let's cycle through each name and then build SIP trunks for each of those for all of the others
	for _, podname := range podNames {
		logrus.Infof("-- Creating trunks for: %v", podname)
		for eachpodname, podip := range podIPs {
			if eachpodname != podname {
				err := createSIPTrunk(podname, podIPs[podname], eachpodname, podip)
				if err != nil {
					return err
				}
			}
		}
	}

	return nil

}

func createSIPTrunk(targetHostName string, targetHostIP string, endpointName string, endpointIP string) error {
	logrus.Infof("Trunk to %v -> %v (on %v @ %v)", endpointName, endpointIP, targetHostName, targetHostIP)

	// Create the http client.
	client := &http.Client{}

	// Setup the URLs
	sorceryURL := fmt.Sprintf("http://asterisk:asterisk@%s:8088", targetHostIP)
	testURL := fmt.Sprintf("%s%s%s", sorceryURL, "/ari/asterisk/config/dynamic/res_pjsip", endpointName)
	endPointURL := fmt.Sprintf("%s%s%s", sorceryURL, "/ari/asterisk/config/dynamic/res_pjsip/endpoint/", endpointName)
	identifyURL := fmt.Sprintf("%s%s%s", sorceryURL, "/ari/asterisk/config/dynamic/res_pjsip/identify/", endpointName)
	aorsURL := fmt.Sprintf("%s%s%s", sorceryURL, "/ari/asterisk/config/dynamic/res_pjsip/aor/", endpointName)

	// TODO: Properly delete before add?
	// TODO: This is inefficient in the sense that it's brute force, it does every config every time.

	// ------------------ WAIT FOR ASTERISK BOOTED.

	// Setup some maximums for this loop.
	tries := 0
	maxtries := 40

	// Loop until we see a properly booted asterisk instance.
	for {
		response, _ := http.Get(testURL)
		testdata, _ := ioutil.ReadAll(response.Body)
		testdatastring := string(testdata)
		if strings.Contains(testdatastring, "Invalid method") {
			// That's good to go, else, keep going.
			break
		}

		// Assess number of tries waiting for success...
		tries++
		if tries >= maxtries {
			return fmt.Errorf("Exceeded %v tries during wait for asterisk boot", maxtries)
		}
		logrus.Infof("Waiting for asterisk boot... %v/%v", tries, maxtries)
		time.Sleep(500 * time.Millisecond)
	}

	// ------------------ ENDPOINTS

	jsonString := fmt.Sprintf(`{
		"fields": [
			{ "attribute": "transport", "value": "transport-udp" },
			{ "attribute": "context", "value": "inbound" },
			{ "attribute": "aors", "value": "%s" },
			{ "attribute": "disallow", "value": "all" },
			{ "attribute": "allow", "value": "ulaw" }
		]	
	}`, endpointName)

	// response, err = http.Post(endPointURL, "application/json", bytes.NewBuffer(jsonValue))
	req, err := http.NewRequest(http.MethodPut, endPointURL, bytes.NewBuffer([]byte(jsonString)))
	req.Header.Set("Content-Type", "application/json")
	_, err := client.Do(req)
	// data, _ := ioutil.ReadAll(response.Body)
	// logrus.Infof("Endpoint result: %v", string(data))

	if err != nil {
		logrus.Errorf("The HTTP request for endpoint failed with error %s", err)
	}

	// ------------------ INDENTITIES

	jsonString = fmt.Sprintf(`{
		"fields": [
			{ "attribute": "endpoint", "value": "%s" },
			{ "attribute": "match", "value": "%s" }
		]	
	}`, endpointName, fmt.Sprintf("%s/%s", endpointIP, "255.255.255.255"))

	req, err = http.NewRequest(http.MethodPut, identifyURL, bytes.NewBuffer([]byte(jsonString)))
	req.Header.Set("Content-Type", "application/json")
	_, err = client.Do(req)
	// data, _ = ioutil.ReadAll(response.Body)
	// logrus.Infof("identify result: %v", string(data))

	if err != nil {
		logrus.Errorf("The HTTP request for identify failed with error %s", err)
	}

	// ------------------ AORS

	jsonString = fmt.Sprintf(`{
		"fields": [
			{ "attribute": "contact", "value": "%s" }
		]	
	}`, fmt.Sprintf("sip:anyuser@%v:5060", endpointIP))

	req, err = http.NewRequest(http.MethodPut, aorsURL, bytes.NewBuffer([]byte(jsonString)))
	req.Header.Set("Content-Type", "application/json")
	_, err = client.Do(req)
	// data, _ = ioutil.ReadAll(response.Body)
	// logrus.Infof("aors result: %v", string(data))

	if err != nil {
		logrus.Errorf("The HTTP request for aors failed with error %s", err)
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
						Image: "dougbtv/asterisk-example-operator",
						// Image: "dougbtv/asterisk14",
						Name: "asterisk",
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

// getPodIPs returns a map of the pod names and their IPs
func getPodIPs(pods []v1.Pod) map[string]string {
	podIPs := make(map[string]string)
	for _, pod := range pods {
		// logrus.Infof("!bang pod everything: %v", pod)
		// logrus.Infof("!bang pod IP: %v", pod.Status.PodIP)
		podIPs[pod.Name] = pod.Status.PodIP // append(podIPs, pod.Status.PodIP)
	}
	return podIPs
}

// getPodNames returns the pod names of the array of pods passed in
func getPodNames(pods []v1.Pod) []string {
	var podNames []string
	for _, pod := range pods {
		// logrus.Infof("!bang pod everything: %v", pod)
		// logrus.Infof("!bang pod IP: %v", pod.Status.PodIP)
		podNames = append(podNames, pod.Name)
	}
	return podNames
}
