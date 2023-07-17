package qworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/galleybytes/terraform-operator-api/pkg/util"
	tfv1beta1 "github.com/galleybytes/terraform-operator/pkg/apis/tf/v1beta1"
	tfo "github.com/galleybytes/terraform-operator/pkg/client/clientset/versioned"
	"github.com/gammazero/deque"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sjson "k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	terraformResource = schema.GroupVersionResource{
		Group:    tfv1beta1.SchemeGroupVersion.Group,
		Version:  tfv1beta1.SchemeGroupVersion.Version,
		Resource: "terraforms",
	}
)

// Listens to the terraform resource queue without blocking
func BackgroundWorker(queue *deque.Deque[tfv1beta1.Terraform]) {
	go worker(queue)
}

func kubernetesConfig(kubeconfigPath string) *rest.Config {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		log.Fatal("Failed to get config for clientset")
	}
	return config
}

func worker(queue *deque.Deque[tfv1beta1.Terraform]) {
	log.Println("Starting queue listener")
	ctx := context.TODO()
	kubeconfig := os.Getenv("KUBECONFIG")
	config := kubernetesConfig(kubeconfig)
	dynamicClient := dynamic.NewForConfigOrDie(config)
	k8sclient := kubernetes.NewForConfigOrDie(config)

	for {
		if queue.Len() == 0 {
			time.Sleep(15 * time.Second)
			continue
		}
		tf := queue.PopFront()

		log.Printf("Will do work with %s/%s", tf.Namespace, tf.Name)

		tenantId := "internal"
		clusterName := tf.Labels["tfo-api.galleybytes.com/cluster-name"]
		if clusterName == "" {
			// The terraform resource requires a cluster name in order to continue
			log.Println("The desired resource is missing a cluster-name identifier. Will not create resource")
			continue
		}

		// Get the host cluster's tf resources to check if we accidentally queued it up
		if unstructedTerraformList, err := dynamicClient.Resource(terraformResource).List(ctx, metav1.ListOptions{}); err == nil {
			terraformList := convertTo[tfv1beta1.TerraformList](unstructedTerraformList)
			isUUIDBelongToThisCluster := false
			for _, terraform := range terraformList.Items {
				if terraform.UID == tf.UID {
					isUUIDBelongToThisCluster = true
					break
				}
			}
			if isUUIDBelongToThisCluster {
				// The UUID matches another UUID which only happens for resources creatd for this cluster
				log.Println("This resource is not managed by the API")
				continue
			}
		}

		// With the clusterName, check out the vcluster config
		vclusterNamespace := tenantId + "-" + clusterName
		secret, err := k8sclient.CoreV1().Secrets(vclusterNamespace).Get(ctx, "vc-tfo-virtual-cluster", metav1.GetOptions{})
		if err != nil {
			if kerrors.IsNotFound(err) {
				requeue(queue, tf, fmt.Sprintln("An error occurred getting vcluster config"))
				continue
			}
			continue
		}
		kubeConfigData := secret.Data["config"]
		if len(kubeConfigData) == 0 {
			requeue(queue, tf, fmt.Sprintln("The vcluster config data was empty"))
			continue
		}

		kubeConfigFilename, err := os.CreateTemp(util.Tmpdir(), "kubeconfig-*")
		if err != nil {
			requeue(queue, tf, fmt.Sprintln("The vcluster kubeconfig file could not be created"))
			continue
		}
		defer os.Remove(kubeConfigFilename.Name())

		err = os.WriteFile(kubeConfigFilename.Name(), kubeConfigData, 0755)
		if err != nil {
			requeue(queue, tf, fmt.Sprintln("The vcluster kubeconfig file could not be saved"))
			continue
		}

		// We have to make an insecure request to the cluster because the vcluster has a cert thats valid for localhost.
		// To do so we set the config to insecure and we remove the CAData. We have to leave
		// CertData and CertFile which are used as authorization to the vcluster.
		vclusterConfig := kubernetesConfig(kubeConfigFilename.Name())
		vclusterConfig.Host = fmt.Sprintf("tfo-virtual-cluster.%s.svc", vclusterNamespace)
		vclusterConfig.Insecure = true
		vclusterConfig.TLSClientConfig.CAData = nil
		vclusterClient := kubernetes.NewForConfigOrDie(vclusterConfig)
		vclusterTFOClient := tfo.NewForConfigOrDie(vclusterConfig)

		// Try and create the namespace for the tfResource. Acceptable error is if namespace already exists.
		_, err = vclusterClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: tf.Namespace,
			},
		}, metav1.CreateOptions{})
		if err != nil {
			if !kerrors.IsAlreadyExists(err) {
				requeue(queue, tf, fmt.Sprintf("The namespace '%s' could not be created in vcluster: %s", tf.Namespace, err))
				return
			}
		}

		// Get a list of resources in the vcluster to see if the resource already exists to determines whether to patch or create.
		terraforms, err := vclusterTFOClient.TfV1beta1().Terraforms(tf.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			requeue(queue, tf, fmt.Sprintf("An error occurred listing tf objects in vcluster: %s", err))
			continue
		}
		isNameExists := false
		for _, terraform := range terraforms.Items {
			if terraform.Name == tf.Name {
				log.Printf("Found %s/%s in %s's vcluster", tf.Namespace, tf.Name, clusterName)
				isNameExists = true
				break
			}
		}

		// Cleanup fields that shouldn't exist when creating or patching resources
		tf.SetResourceVersion("")
		tf.SetUID("")
		tf.SetSelfLink("")
		tf.SetGeneration(0)
		tf.SetManagedFields(nil)
		tf.SetCreationTimestamp(metav1.Time{})
		if isNameExists {
			// TODO Do JSONPatch which should catch the removal or changes of boolean fields that are being missed by patch
			err := doPatch(&tf, ctx, tf.Name, tf.Namespace, vclusterTFOClient)
			if err != nil {
				requeue(queue, tf, fmt.Sprintf("An error occurred patching tf object: %v", err))
				continue
			}
			log.Printf("terraform %s/%s patched", tf.Namespace, tf.Name)
			continue
		}

		err = doCreate(tf, ctx, tf.Namespace, vclusterTFOClient)
		if err != nil {
			requeue(queue, tf, fmt.Sprintf("Error creating new tf resource: %v", err))
			continue
		}
		log.Printf("Added terraform resource '%s' ('%s/%s')", tf.Name, tf.Namespace, tf.Name)
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

func convertTerraformToUnstructuredObject(terraform tfv1beta1.Terraform) (*unstructured.Unstructured, error) {
	unstructuredObject := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": tfv1beta1.SchemeGroupVersion.String(),
			"kind":       "Terraform",
			"metadata":   terraform.ObjectMeta,
			"spec":       terraform.Spec,
		},
	}
	return &unstructuredObject, nil
}

func doCreate(new tfv1beta1.Terraform, ctx context.Context, namespace string, client tfo.Interface) error {
	_, err := client.TfV1beta1().Terraforms(namespace).Create(ctx, &new, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("an error occurred saving tf object: %v", err)
	}
	return nil
}

func patchableTFResource(obj *tfv1beta1.Terraform) []byte {
	gvks, _, err := scheme.Scheme.ObjectKinds(obj)
	if err != nil || len(gvks) == 0 {
		obj.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   tfv1beta1.SchemeGroupVersion.Group,
			Version: tfv1beta1.SchemeGroupVersion.Version,
			Kind:    "Terraform",
		})
	}

	buf := bytes.NewBuffer([]byte{})
	k8sjson.NewSerializer(k8sjson.DefaultMetaFactory, runtime.NewScheme(), runtime.NewScheme(), true).Encode(obj, buf)
	return buf.Bytes()
}

func doPatch(new *tfv1beta1.Terraform, ctx context.Context, name, namespace string, client tfo.Interface) error {
	// Apply new changes from awsNodeTemplate into awsNodeTemplateOld
	log.Printf("will patch %s/%s form tf resource that has name=%s and namespace=%s", namespace, name, new.Name, new.Namespace)
	_, err := client.TfV1beta1().Terraforms(namespace).Patch(ctx, name, types.MergePatchType, patchableTFResource(new), metav1.PatchOptions{
		FieldManager: name,
	})
	if err != nil {
		return err
	}
	return nil
}

func doUpdate(new tfv1beta1.Terraform, ctx context.Context, namespace, name string, client tfo.Interface) error {
	// Apply new changes from awsNodeTemplate into awsNodeTemplateOld
	delete(new.Annotations, "kubectl.kubernetes.io/last-applied-configuration")
	_, err := client.TfV1beta1().Terraforms(namespace).Update(ctx, &new, metav1.UpdateOptions{})
	if err != nil {
		log.Println(err)
		return err
	}
	return nil
}

func requeue(queue *deque.Deque[tfv1beta1.Terraform], tf tfv1beta1.Terraform, reason string) {
	log.Println(reason)
	go func() {
		time.Sleep(15 * time.Second)
		queue.PushBack(tf)
	}()
}

// modSpec handles required changes to prevent conflicts in the hub cluster due to the sharing nature of workspaces
func modSpec(tf tfv1beta1.Terraform, modTf *tfv1beta1.Terraform) {
	if tf.Spec.OutputsSecret != "" {
		addAnnotation(modTf, "tfo.galleybytes.com/outputsSecret", tf.Spec.OutputsSecret)
		modTf.Spec.OutputsSecret = string(uuid.NewUUID())
	}
}

// addAnnotation adds annotations to the metadata
func addAnnotation(modTf *tfv1beta1.Terraform, key, value string) {
	if modTf.Annotations == nil {
		modTf.Annotations = map[string]string{}
	}
	modTf.Annotations[key] = value
}

// modLabels adds labels to the metadata
func addLabel(modTf *tfv1beta1.Terraform, key, value string) {
	if modTf.Labels == nil {
		modTf.Labels = map[string]string{}
	}
	modTf.Labels[key] = value
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
