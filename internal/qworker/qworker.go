package qworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gammazero/deque"
	tfv1alpha2 "github.com/isaaguilar/terraform-operator/pkg/apis/tf/v1alpha2"
	"github.com/mattbaird/jsonpatch"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	terraformResource = schema.GroupVersionResource{
		Group:    tfv1alpha2.SchemeGroupVersion.Group,
		Version:  tfv1alpha2.SchemeGroupVersion.Version,
		Resource: "terraforms",
	}
)

// Listens to the terraform resource queue without blocking
func BackgroundWorker(queue *deque.Deque[tfv1alpha2.Terraform]) {
	go worker(queue)
}

func kubernetesConfig(kubeconfigPath string) *rest.Config {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		log.Fatal("Failed to get config for clientset")
	}
	return config
}

func worker(queue *deque.Deque[tfv1alpha2.Terraform]) {
	log.Println("Starting queue listener")
	ctx := context.TODO()
	kubeconfig := os.Getenv("KUBECONFIG")
	config := kubernetesConfig(kubeconfig)
	dynamicClient := dynamic.NewForConfigOrDie(config)

	for {
		if queue.Len() == 0 {
			time.Sleep(15 * time.Second)
			continue
		}
		tf := queue.PopFront()

		log.Printf("Will do work with %s/%s", tf.Namespace, tf.Name)

		// List resources and check
		// 1) UUID exists or
		// 2) UUID-name exists
		resourceClient := dynamicClient.Resource(terraformResource)
		unstructedTerraformList, err := resourceClient.List(ctx, metav1.ListOptions{})
		if err != nil {
			continue
		}
		terraformList := convertTo[tfv1alpha2.TerraformList](unstructedTerraformList)
		indexOfExisting := 0
		isNameExists := false
		isUUIDBelongToThisCluster := false
		for i, terraform := range terraformList.Items {
			if terraform.UID == tf.UID {
				isUUIDBelongToThisCluster = true
				break
			}
			if terraform.Name == string(tf.UID) {
				isNameExists = true
				indexOfExisting = i
				break
			}
		}
		if isUUIDBelongToThisCluster {
			// The UUID matches another UUID which only happens for resources creatd for this cluster
			log.Println("This resource is not managed by the API")
			continue
		}

		namespace := "default"
		labels := tf.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		labels["tfo-api.galleybytes.com/original-resource-name"] = tf.Name
		labels["tfo-api.galleybytes.com/original-resource-namespace"] = tf.Namespace
		modTf := tfv1alpha2.Terraform{
			ObjectMeta: metav1.ObjectMeta{
				Name:        string(tf.UID),
				Namespace:   namespace,
				Annotations: tf.Annotations,
				Labels:      labels,
			},
			Spec: tf.Spec,
		}

		if isNameExists {
			realTf := terraformList.Items[indexOfExisting]
			err := patch(realTf, modTf, ctx, realTf.Name, resourceClient.Namespace(namespace))
			if err != nil {
				log.Printf("An error occurred patching tf object: %v", err)
				go func() {
					time.Sleep(15 * time.Second)
					queue.PushBack(tf)
				}()
				continue
			}
			continue
		}

		log.Println("Work needs to be done to ADD the resource")
		unstructuredTerraform, err := convertTerraformToUnstructuredObject(modTf)
		if err != nil {
			log.Printf("An error occurred formatting tf object for saving: %v", err)
			go func() {
				time.Sleep(15 * time.Second)
				queue.PushBack(tf)
			}()
			continue
		}

		_, err = dynamicClient.Resource(terraformResource).Namespace("default").Create(ctx, unstructuredTerraform, metav1.CreateOptions{})
		if err != nil {
			log.Printf("An error occurred saving tf object: %v", err)
			go func() {
				time.Sleep(15 * time.Second)
				queue.PushBack(tf)
			}()
			continue
		}
		log.Printf("Added terraform resource '%s' ('%s/%s')", modTf.Name, tf.Namespace, tf.Name)
	}

}

func convertTo[T any](unstructured any) T {
	b, err := json.Marshal(unstructured)
	if err != nil {
		log.Panic(err)
	}
	var t T
	err = json.Unmarshal(b, &t)
	if err != nil {
		log.Panic(err)
	}
	return t
}

func convertTerraformToUnstructuredObject(terraform tfv1alpha2.Terraform) (*unstructured.Unstructured, error) {
	unstructuredObject := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": tfv1alpha2.SchemeGroupVersion.String(),
			"kind":       "Terraform",
			"metadata":   terraform.ObjectMeta,
			"spec":       terraform.Spec,
		},
	}
	return &unstructuredObject, nil
}

func patch[T any](old, new T, ctx context.Context, name string, client dynamic.ResourceInterface) error {
	// Apply new changes from awsNodeTemplate into awsNodeTemplateOld

	oldJSON, err := json.Marshal(old)
	if err != nil {
		return err
	}

	err = json.Unmarshal(oldJSON, &new)
	if err != nil {
		return err
	}
	newJSON, err := json.Marshal(new)
	if err != nil {
		return err
	}
	patch, err := jsonpatch.CreatePatch(oldJSON, newJSON)
	if err != nil {
		return err
	}
	if len(patch) == 0 {
		return nil
	}
	for _, p := range patch {
		log.Println(p)
	}
	jsonPatch, err := json.Marshal(patch)
	if err != nil {
		return err
	}

	_, err = client.Patch(ctx, name, types.JSONPatchType, jsonPatch, metav1.PatchOptions{})
	if err != nil {
		return err
	}
	log.Printf("terraform/%s patched", name)
	return nil
}

func pprint(o interface{}) {
	b, err := json.Marshal(o)
	if err != nil {
		log.Panic(err)
	}
	var out bytes.Buffer
	err = json.Indent(&out, b, "", "  ")
	if err != nil {
		log.Panic(err)
	}
	fmt.Println(out.String())
}
