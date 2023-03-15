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
			requeue(queue, tf, fmt.Sprintln("An error occurred listing tf objects"))
			continue
		}
		terraformList := convertTo[tfv1alpha2.TerraformList](unstructedTerraformList)
		isNameExists := false
		isUUIDBelongToThisCluster := false
		for _, terraform := range terraformList.Items {
			if terraform.UID == tf.UID {
				isUUIDBelongToThisCluster = true
				break
			}
			if terraform.Name == string(tf.UID) {
				isNameExists = true
				break
			}
		}
		if isUUIDBelongToThisCluster {
			// The UUID matches another UUID which only happens for resources creatd for this cluster
			log.Println("This resource is not managed by the API")
			continue
		}

		name := string(tf.UID)
		namespace := "default"
		labels := tf.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		labels["tfo-api.galleybytes.com/original-resource-name"] = tf.Name
		labels["tfo-api.galleybytes.com/original-resource-namespace"] = tf.Namespace

		// modTf as in the modified terraform resource that get added to the remote cluster. This cluster can't have the
		// same name as any other resource, so we use the uid or the original resource as a unique name for this one.
		var modTf = tfv1alpha2.Terraform{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Namespace:   namespace,
				Annotations: tf.Annotations,
				Labels:      labels,
			},
			Spec: tf.Spec,
		}

		if isNameExists {
			err := doPatch(modTf, ctx, name, namespace, resourceClient)
			if err != nil {
				requeue(queue, tf, fmt.Sprintf("An error occurred patching tf object: %v", err))
				continue
			}
			log.Printf("terraform %s/%s patched", namespace, name)
			continue
		}

		err = doCreate(modTf, ctx, namespace, resourceClient)
		if err != nil {
			requeue(queue, tf, fmt.Sprintf("Error creating new tf resource: %v", err))
			continue
		}
		log.Printf("Added terraform resource '%s' ('%s/%s')", name, tf.Namespace, tf.Name)
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

func doCreate(new tfv1alpha2.Terraform, ctx context.Context, namespace string, client dynamic.NamespaceableResourceInterface) error {
	unstructuredTerraform, err := convertTerraformToUnstructuredObject(new)
	if err != nil {
		return fmt.Errorf("an error occurred formatting tf object for saving: %v", err)
	}
	_, err = client.Namespace(namespace).Create(ctx, unstructuredTerraform, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("an error occurred saving tf object: %v", err)
	}
	return nil
}

func doPatch(new any, ctx context.Context, name, namespace string, client dynamic.NamespaceableResourceInterface) error {
	// Apply new changes from awsNodeTemplate into awsNodeTemplateOld
	inputJSON, err := json.Marshal(new)
	if err != nil {
		return err
	}
	_, err = client.Namespace(namespace).Patch(ctx, name, types.MergePatchType, inputJSON, metav1.PatchOptions{})
	if err != nil {
		return err
	}
	return nil
}

func requeue(queue *deque.Deque[tfv1alpha2.Terraform], tf tfv1alpha2.Terraform, reason string) {
	log.Printf("An error occurred saving tf object: %s", reason)
	go func() {
		time.Sleep(15 * time.Second)
		queue.PushBack(tf)
	}()
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
