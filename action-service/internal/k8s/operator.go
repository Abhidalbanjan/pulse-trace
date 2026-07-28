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

	// policy is this operator's remediation posture. It is enforced here, at
	// the point commands actually reach the cluster, rather than trusting the
	// caller to have checked — the caller is an HTTP client on the other side
	// of a network boundary.
	policy Policy
}

func NewOperator() *Operator {
	// policyFromEnv falls back to the restrictive default and logs anything it
	// can't parse, so a typo'd REMEDIATION_MODE never widens permissions.
	return newOperatorWithPolicy(policyFromEnv())
}

func newOperatorWithPolicy(policy Policy) *Operator {
	// K8S_TARGET_NAMESPACE is only the *fallback* namespace, used when a remediation
	// action doesn't name one. Real clusters spread monitored services across many
	// namespaces, so the namespace to act in is resolved per-action (from the
	// service's topology/incident record, passed in by the caller) — see
	// namespaceFor. A single fixed namespace here could only ever remediate one.
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
			return &Operator{namespace: namespace, policy: policy}
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Printf("[K8s Operator] WARNING: Failed to create clientset: %v. Running in MOCK mode.", err)
		return &Operator{namespace: namespace, policy: policy}
	}

	return &Operator{
		clientset: clientset,
		namespace: namespace,
		policy:    policy,
	}
}

// Policy exposes the operator's resolved remediation posture so the HTTP layer
// can report it without re-reading env vars.
func (o *Operator) Policy() Policy { return o.policy }

// namespaceFor resolves the namespace a given action should run in: the
// caller-supplied namespace (from the target service's topology record) when
// present, otherwise the operator's configured fallback. This is what lets one
// action-service remediate services spread across many namespaces.
func (o *Operator) namespaceFor(namespace string) string {
	if namespace != "" {
		return namespace
	}
	return o.namespace
}

// Result describes the outcome of a remediation request.
type Result struct {
	// DryRun is true when nothing was changed — either because the caller
	// asked for a plan, or because this operator's policy forbids execution.
	DryRun bool `json:"dry_run"`
	// Plan is the human-readable description of what would run (dry runs) or
	// what did run.
	Plan string `json:"plan"`
}

// ExecuteRunbook performs a specific cluster action (e.g., rolling back a
// deployment) against `namespace` (falling back to the operator default when
// empty — see namespaceFor).
//
// dryRun computes the change and reports it without applying it. The operator
// upgrades a live request to a dry run whenever its own policy forbids
// execution: this service is what actually mutates the cluster, so it is the
// right place to enforce the posture rather than trusting its HTTP caller.
func (o *Operator) ExecuteRunbook(actionType, target, namespace string, parameters map[string]string, dryRun bool) (Result, error) {
	ns := o.namespaceFor(namespace)

	if !dryRun && !o.policy.AllowsExecution() {
		log.Printf("[K8s Operator] Downgrading a live request to dry-run — REMEDIATION_MODE is %q", o.policy.Mode)
		dryRun = true
	}

	verb := "Executing"
	if dryRun {
		verb = "Planning (dry-run)"
	}
	log.Printf("[K8s Operator] %s Action: %s | Target: %s/%s | Params: %v", verb, actionType, ns, target, parameters)

	if o.clientset == nil {
		log.Printf("[K8s Operator] MOCK Execution (No K8s cluster): %s on %s/%s", actionType, ns, target)
		return Result{
			DryRun: true,
			Plan:   fmt.Sprintf("No Kubernetes cluster is reachable; %s on %s/%s was not performed.", actionType, ns, target),
		}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	deploymentsClient := o.clientset.AppsV1().Deployments(ns)

	switch actionType {
	case "ROLLBACK":
		// This previously fetched the deployment, logged "Successfully executed
		// Rollback", and returned nil without changing anything — a fake success.
		// This real implementation mirrors `kubectl rollout undo`: find the
		// ReplicaSet for the previous revision and restore its pod template.
		//
		// Every step up to the final Update is read-only, so a dry run does all
		// the same resolution work and reports the exact revision it would roll
		// back to — a plan worth reviewing, not a restatement of the request.
		deployment, err := deploymentsClient.Get(ctx, target, metav1.GetOptions{})
		if err != nil {
			return Result{}, fmt.Errorf("failed to get deployment %s: %w", target, err)
		}

		selector, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector)
		if err != nil {
			return Result{}, fmt.Errorf("invalid selector on deployment %s: %w", target, err)
		}

		rsList, err := o.clientset.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{
			LabelSelector: selector.String(),
		})
		if err != nil {
			return Result{}, fmt.Errorf("failed to list replicasets for %s: %w", target, err)
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
			return Result{}, fmt.Errorf("no revision history found for deployment %s — nothing to roll back to", target)
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].revision > candidates[j].revision })

		currentRev, _ := strconv.ParseInt(deployment.Annotations["deployment.kubernetes.io/revision"], 10, 64)

		var chosen *revisionedRS
		if toRev := parameters["revision"]; toRev != "" {
			// Explicit target revision — parity with `kubectl rollout undo --to-revision=N`.
			wantRev, err := strconv.ParseInt(toRev, 10, 64)
			if err != nil {
				return Result{}, fmt.Errorf("invalid revision parameter %q: %w", toRev, err)
			}
			for i := range candidates {
				if candidates[i].revision == wantRev {
					chosen = &candidates[i]
					break
				}
			}
			if chosen == nil {
				return Result{}, fmt.Errorf("revision %d not found in history for %s", wantRev, target)
			}
		} else {
			// Default: the most recent revision that isn't the deployment's
			// current one — i.e. "undo the last change".
			for i := range candidates {
				if candidates[i].revision != currentRev {
					chosen = &candidates[i]
					break
				}
			}
			if chosen == nil {
				return Result{}, fmt.Errorf("no earlier revision found for deployment %s — already at oldest known revision", target)
			}
		}

		plan := fmt.Sprintf("Roll back deployment %s/%s from revision %d to revision %d (pod template from ReplicaSet %s).",
			ns, target, currentRev, chosen.revision, chosen.rs.Name)
		if dryRun {
			return dryRunResult(plan), nil
		}

		deployment.Spec.Template = chosen.rs.Spec.Template
		if deployment.Annotations == nil {
			deployment.Annotations = make(map[string]string)
		}
		deployment.Annotations["pulsetrace.io/rolled-back-from-revision"] = deployment.Annotations["deployment.kubernetes.io/revision"]

		if _, err := deploymentsClient.Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
			return Result{}, fmt.Errorf("failed to apply rollback for %s: %w", target, err)
		}

		log.Printf("[K8s Operator] Successfully rolled back %s to revision %d", target, chosen.revision)
		return Result{Plan: plan}, nil

	case "RESTART_PODS":
		// A restart is achieved by patching the pod template with a new
		// annotation, which is what triggers a rolling replacement.
		deployment, err := deploymentsClient.Get(ctx, target, metav1.GetOptions{})
		if err != nil {
			return Result{}, fmt.Errorf("failed to get deployment %s: %w", target, err)
		}

		replicas := int32(1)
		if deployment.Spec.Replicas != nil {
			replicas = *deployment.Spec.Replicas
		}
		plan := fmt.Sprintf("Trigger a rolling restart of deployment %s/%s (%d replica(s)) by stamping the pod template with kubectl.kubernetes.io/restartedAt.",
			ns, target, replicas)
		if dryRun {
			return dryRunResult(plan), nil
		}

		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = make(map[string]string)
		}
		deployment.Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"] = time.Now().Format(time.RFC3339)
		if _, err := deploymentsClient.Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
			return Result{}, fmt.Errorf("failed to restart deployment %s: %w", target, err)
		}
		log.Printf("[K8s Operator] Successfully restarted pods for %s", target)
		return Result{Plan: plan}, nil

	case "SCALE":
		replicas, err := strconv.Atoi(parameters["replicas"])
		if err != nil {
			return Result{}, fmt.Errorf("invalid replicas parameter: %v", err)
		}

		scale, err := deploymentsClient.GetScale(ctx, target, metav1.GetOptions{})
		if err != nil {
			return Result{}, fmt.Errorf("failed to get scale for %s: %w", target, err)
		}

		// Reporting the current replica count makes the plan reviewable: "8 → 2"
		// is a decision an on-call engineer can sanity-check, "scale to 2" is not.
		plan := fmt.Sprintf("Scale deployment %s/%s from %d to %d replica(s).",
			ns, target, scale.Spec.Replicas, replicas)
		if dryRun {
			return dryRunResult(plan), nil
		}

		scale.Spec.Replicas = int32(replicas)
		if _, err := deploymentsClient.UpdateScale(ctx, target, scale, metav1.UpdateOptions{}); err != nil {
			return Result{}, fmt.Errorf("failed to scale deployment %s: %w", target, err)
		}

		log.Printf("[K8s Operator] Successfully scaled %s to %d replicas", target, replicas)
		return Result{Plan: plan}, nil

	default:
		// Unknown actions fail in both modes — a dry run that reported "nothing
		// to do" would hide the misconfiguration.
		return Result{}, fmt.Errorf("unknown action type: %s", actionType)
	}
}

// dryRunResult wraps a plan as an unexecuted result.
func dryRunResult(plan string) Result {
	return Result{DryRun: true, Plan: "DRY RUN — nothing was changed. Would: " + plan}
}
