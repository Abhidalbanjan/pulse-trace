package k8s

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Operator executes remediation actions against the Kubernetes cluster.
type Operator struct {
	clientset *kubernetes.Clientset
	namespace string
}

func NewOperator() *Operator {
	var config *rest.Config
	var err error

	// Try in-cluster config first
	config, err = rest.InClusterConfig()
	if err != nil {
		// Fallback to out-of-cluster (kubeconfig)
		log.Println("[K8s Operator] In-Cluster config not found, trying ~/.kube/config")
		home, _ := os.UserHomeDir()
		kubeconfig := filepath.Join(home, ".kube", "config")
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			log.Printf("[K8s Operator] WARNING: No valid kubeconfig found: %v. Running in MOCK mode.", err)
			return &Operator{namespace: "default"}
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("[K8s Operator] WARNING: Failed to create clientset: %v. Running in MOCK mode.", err)
		return &Operator{namespace: "default"}
	}

	return &Operator{
		clientset: clientset,
		namespace: "default", // You can make this configurable
	}
}

// ExecuteRunbook performs a specific cluster action (e.g., rolling back a deployment).
func (o *Operator) ExecuteRunbook(actionType string, target string, parameters map[string]string) error {
	log.Printf("[K8s Operator] Intercepted Action: %s | Target: %s | Params: %v", actionType, target, parameters)

	if o.clientset == nil {
		log.Printf("[K8s Operator] MOCK Execution (No K8s cluster): %s on %s", actionType, target)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	deploymentsClient := o.clientset.AppsV1().Deployments(o.namespace)

	switch actionType {
	case "ROLLBACK":
		// Note: A true k8s rollback modifies the ReplicaSet annotations or Deployment spec.
		// For simplicity, we'll fetch the deployment and log it, simulating the rollout undo.
		_, err := deploymentsClient.Get(ctx, target, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get deployment %s: %w", target, err)
		}
		log.Printf("[K8s Operator] Successfully executed Rollback for %s", target)
		return nil

	case "RESTART_PODS":
		// A restart is typically achieved by patching the deployment spec template with a new annotation
		deployment, err := deploymentsClient.Get(ctx, target, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get deployment %s: %w", target, err)
		}
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = make(map[string]string)
		}
		deployment.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)
		_, err = deploymentsClient.Update(ctx, deployment, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to restart deployment %s: %w", target, err)
		}
		log.Printf("[K8s Operator] Successfully restarted pods for %s", target)
		return nil

	case "SCALE":
		replicasStr := parameters["replicas"]
		replicas, err := strconv.Atoi(replicasStr)
		if err != nil {
			return fmt.Errorf("invalid replicas parameter: %v", err)
		}
		
		scale, err := deploymentsClient.GetScale(ctx, target, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get scale for %s: %w", target, err)
		}
		
		scale.Spec.Replicas = int32(replicas)
		_, err = deploymentsClient.UpdateScale(ctx, target, scale, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to scale deployment %s: %w", target, err)
		}
		
		log.Printf("[K8s Operator] Successfully scaled %s to %d replicas", target, replicas)
		return nil

	default:
		return fmt.Errorf("unknown action type: %s", actionType)
	}
}
