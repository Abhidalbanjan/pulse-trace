package k8s

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
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
	// K8S_TARGET_NAMESPACE is the namespace this operator remediates deployments
	// in — i.e. where the *customer's* monitored services actually run, which
	// is essentially never "default" in a real deployment. This was previously
	// hardcoded to "default", so in any real cluster this operator would only
	// ever be able to see deployments in a namespace nothing runs in.
	namespace := os.Getenv("K8S_TARGET_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}

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
			return &Operator{namespace: namespace}
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("[K8s Operator] WARNING: Failed to create clientset: %v. Running in MOCK mode.", err)
		return &Operator{namespace: namespace}
	}

	return &Operator{
		clientset: clientset,
		namespace: namespace,
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
		// This previously fetched the deployment, logged "Successfully executed
		// Rollback", and returned nil without changing anything — a fake success.
		// This real implementation mirrors `kubectl rollout undo`: find the
		// ReplicaSet for the previous revision and restore its pod template.
		deployment, err := deploymentsClient.Get(ctx, target, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get deployment %s: %w", target, err)
		}

		selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
		if err != nil {
			return fmt.Errorf("invalid selector on deployment %s: %w", target, err)
		}

		rsList, err := o.clientset.AppsV1().ReplicaSets(o.namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector.String(),
		})
		if err != nil {
			return fmt.Errorf("failed to list replicasets for %s: %w", target, err)
		}

		type revisionedRS struct {
			rs       appsv1.ReplicaSet
			revision int64
		}
		var candidates []revisionedRS
		for _, rs := range rsList.Items {
			owned := false
			for _, ref := range rs.OwnerReferences {
				if ref.Kind == "Deployment" && ref.Name == deployment.Name && ref.UID == deployment.UID {
					owned = true
					break
				}
			}
			if !owned {
				continue
			}
			revStr, ok := rs.Annotations["deployment.kubernetes.io/revision"]
			if !ok {
				continue
			}
			rev, err := strconv.ParseInt(revStr, 10, 64)
			if err != nil {
				continue
			}
			candidates = append(candidates, revisionedRS{rs: rs, revision: rev})
		}
		if len(candidates) == 0 {
			return fmt.Errorf("no revision history found for deployment %s — nothing to roll back to", target)
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].revision > candidates[j].revision })

		var chosen *revisionedRS
		if toRev := parameters["revision"]; toRev != "" {
			// Explicit target revision — parity with `kubectl rollout undo --to-revision=N`.
			wantRev, err := strconv.ParseInt(toRev, 10, 64)
			if err != nil {
				return fmt.Errorf("invalid revision parameter %q: %w", toRev, err)
			}
			for i := range candidates {
				if candidates[i].revision == wantRev {
					chosen = &candidates[i]
					break
				}
			}
			if chosen == nil {
				return fmt.Errorf("revision %d not found in history for %s", wantRev, target)
			}
		} else {
			// Default: the most recent revision that isn't the deployment's
			// current one — i.e. "undo the last change".
			currentRev, _ := strconv.ParseInt(deployment.Annotations["deployment.kubernetes.io/revision"], 10, 64)
			for i := range candidates {
				if candidates[i].revision != currentRev {
					chosen = &candidates[i]
					break
				}
			}
			if chosen == nil {
				return fmt.Errorf("no earlier revision found for deployment %s — already at oldest known revision", target)
			}
		}

		deployment.Spec.Template = chosen.rs.Spec.Template
		if deployment.Annotations == nil {
			deployment.Annotations = make(map[string]string)
		}
		deployment.Annotations["pulsetrace.io/rolled-back-from-revision"] = deployment.Annotations["deployment.kubernetes.io/revision"]

		if _, err := deploymentsClient.Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("failed to apply rollback for %s: %w", target, err)
		}

		log.Printf("[K8s Operator] Successfully rolled back %s to revision %d", target, chosen.revision)
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
