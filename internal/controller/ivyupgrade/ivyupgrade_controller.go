// Copyright 2021 - 2023 Highgo Solutions, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ivyupgrade

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/highgo/ivory-operator/pkg/apis/ivory-operator.highgo.com/v1beta1"
)

const (
	AnnotationAllowUpgrade = "ivory-operator.highgo.com/allow-upgrade"
)

// IvyUpgradeReconciler reconciles a IvyUpgrade object
type IvyUpgradeReconciler struct {
	client.Client
	Owner  client.FieldOwner
	Scheme *runtime.Scheme

	// For this iteration, we will only be setting conditions rather than
	// setting conditions and emitting events. That may change in the future,
	// so we're leaving this EventRecorder here for now.
	// record.EventRecorder
}

//+kubebuilder:rbac:groups="batch",resources="jobs",verbs={list,watch}
//+kubebuilder:rbac:groups="ivory-operator.highgo.com",resources="ivyupgrades",verbs={list,watch}
//+kubebuilder:rbac:groups="ivory-operator.highgo.com",resources="ivoryclusters",verbs={list,watch}

// SetupWithManager sets up the controller with the Manager.
func (r *IvyUpgradeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.IvyUpgrade{}).
		Owns(&batchv1.Job{}).
		Watches(
			&source.Kind{Type: v1beta1.NewIvoryCluster()},
			r.watchIvoryClusters(),
		).
		Complete(r)
}

//+kubebuilder:rbac:groups="ivory-operator.highgo.com",resources="ivyupgrades",verbs={list}

// findUpgradesForIvoryCluster returns IvyUpgrades that target cluster.
func (r *IvyUpgradeReconciler) findUpgradesForIvoryCluster(
	ctx context.Context, cluster client.ObjectKey,
) []*v1beta1.IvyUpgrade {
	var matching []*v1beta1.IvyUpgrade
	var upgrades v1beta1.IvyUpgradeList

	// NOTE: If this becomes slow due to a large number of upgrades in a single
	// namespace, we can configure the [ctrl.Manager] field indexer and pass a
	// [fields.Selector] here.
	// - https://book.kubebuilder.io/reference/watching-resources/externally-managed.html
	if r.List(ctx, &upgrades, &client.ListOptions{
		Namespace: cluster.Namespace,
	}) == nil {
		for i := range upgrades.Items {
			if upgrades.Items[i].Spec.IvoryClusterName == cluster.Name {
				matching = append(matching, &upgrades.Items[i])
			}
		}
	}
	return matching
}

// watchIvoryClusters returns a [handler.EventHandler] for IvoryClusters.
func (r *IvyUpgradeReconciler) watchIvoryClusters() handler.Funcs {
	handle := func(cluster client.Object, q workqueue.RateLimitingInterface) {
		ctx := context.Background()
		key := client.ObjectKeyFromObject(cluster)

		for _, upgrade := range r.findUpgradesForIvoryCluster(ctx, key) {
			q.Add(ctrl.Request{
				NamespacedName: client.ObjectKeyFromObject(upgrade),
			})
		}
	}

	return handler.Funcs{
		CreateFunc: func(e event.CreateEvent, q workqueue.RateLimitingInterface) {
			handle(e.Object, q)
		},
		UpdateFunc: func(e event.UpdateEvent, q workqueue.RateLimitingInterface) {
			handle(e.ObjectNew, q)
		},
		DeleteFunc: func(e event.DeleteEvent, q workqueue.RateLimitingInterface) {
			handle(e.Object, q)
		},
	}
}

//+kubebuilder:rbac:groups="ivory-operator.highgo.com",resources="ivyupgrades",verbs={get}
//+kubebuilder:rbac:groups="ivory-operator.highgo.com",resources="ivyupgrades/status",verbs={patch}
//+kubebuilder:rbac:groups="batch",resources="jobs",verbs={delete}
//+kubebuilder:rbac:groups="ivory-operator.highgo.com",resources="ivoryclusters",verbs={get}
//+kubebuilder:rbac:groups="ivory-operator.highgo.com",resources="ivoryclusters/status",verbs={patch}
//+kubebuilder:rbac:groups="batch",resources="jobs",verbs={create,patch}
//+kubebuilder:rbac:groups="batch",resources="jobs",verbs={list}
//+kubebuilder:rbac:groups="",resources="endpoints",verbs={get}
//+kubebuilder:rbac:groups="",resources="endpoints",verbs={delete}

// Reconcile does the work to move the current state of the world toward the
// desired state described in a [v1beta1.IvyUpgrade] identified by req.
func (r *IvyUpgradeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	log := ctrl.LoggerFrom(ctx)

	// Retrieve the upgrade from the client cache, if it exists. A deferred
	// function below will send any changes to its Status field.
	//
	// NOTE: No DeepCopy is necessary here because controller-runtime makes a
	// copy before returning from its cache.
	// - https://github.com/kubernetes-sigs/controller-runtime/issues/1235
	upgrade := &v1beta1.IvyUpgrade{}
	err = r.Get(ctx, req.NamespacedName, upgrade)

	if err == nil {
		// Write any changes to the upgrade status on the way out.
		before := upgrade.DeepCopy()
		defer func() {
			if !equality.Semantic.DeepEqual(before.Status, upgrade.Status) {
				status := r.Status().Patch(ctx, upgrade, client.MergeFrom(before), r.Owner)

				if err == nil && status != nil {
					err = status
				} else if status != nil {
					log.Error(status, "Patching IvyUpgrade status")
				}
			}
		}()
	} else {
		// NotFound cannot be fixed by requeuing so ignore it. During background
		// deletion, we receive delete events from upgrade's dependents after
		// upgrade is deleted.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Validate the remainder of the upgrade specification. These can likely
	// move to CEL rules or a webhook when supported.

	// Exit if upgrade success condition has already been reached.
	// If a cluster needs multiple upgrades, it is currently only possible to delete and
	// create a new ivyupgrade rather than edit an existing succeeded upgrade.
	// This controller may be changed in the future to allow multiple uses of
	// a single ivyupgrade; if that is the case, it will probably need to reset
	// the succeeded condition and remove upgrade and removedata jobs.
	succeeded := meta.FindStatusCondition(upgrade.Status.Conditions,
		ConditionIvyUpgradeSucceeded)
	if succeeded != nil && succeeded.Reason == "IvyUpgradeSucceeded" {
		return
	}

	// Set progressing condition to true if it doesn't exist already
	setStatusToProgressingIfReasonWas("", upgrade)

	// The "from" version must be smaller than the "to" version.
	// An invalid IvyUpgrade should not be requeued.
	if upgrade.Spec.FromIvoryVersion >= upgrade.Spec.ToIvoryVersion {

		meta.SetStatusCondition(&upgrade.Status.Conditions, metav1.Condition{
			ObservedGeneration: upgrade.GetGeneration(),
			Type:               ConditionIvyUpgradeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "IvyUpgradeInvalid",
			Message: fmt.Sprintf(
				"Cannot upgrade from ivory version %d to %d",
				upgrade.Spec.FromIvoryVersion, upgrade.Spec.ToIvoryVersion),
		})

		return ctrl.Result{}, nil
	}

	setStatusToProgressingIfReasonWas("IvyUpgradeInvalid", upgrade)

	// Observations and cluster validation
	//
	// First, read everything we need from the API. Compare the state of the
	// world to the upgrade specification, perform any remaining validation.
	world, err := r.observeWorld(ctx, upgrade)
	// If `observeWorld` returns an error, then exit early.
	// If we do no exit here, err is assume nil
	if err != nil {
		meta.SetStatusCondition(&upgrade.Status.Conditions, metav1.Condition{
			ObservedGeneration: upgrade.Generation,
			Type:               ConditionIvyUpgradeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "PGClusterErrorWhenObservingWorld",
			Message:            err.Error(),
		})

		return // FIXME
	}

	setStatusToProgressingIfReasonWas("PGClusterErrorWhenObservingWorld", upgrade)

	// ClusterNotFound cannot be fixed by requeuing. We will reconcile again when
	// a matching IvoryCluster is created. Set a condition about our
	// inability to proceed.
	if world.ClusterNotFound != nil {

		meta.SetStatusCondition(&upgrade.Status.Conditions, metav1.Condition{
			ObservedGeneration: upgrade.Generation,
			Type:               ConditionIvyUpgradeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "PGClusterNotFound",
			Message:            world.ClusterNotFound.Error(),
		})

		return ctrl.Result{}, nil
	}

	setStatusToProgressingIfReasonWas("PGClusterNotFound", upgrade)

	// Get the spec version to check if this cluster is at the requested version
	version := int64(world.Cluster.Spec.PostgresVersion)

	// Get the status version and check the jobs to see if this upgrade has completed
	statusVersion := int64(world.Cluster.Status.PostgresVersion)
	upgradeJob := world.Jobs[pgUpgradeJob(upgrade).Name]
	upgradeJobComplete := upgradeJob != nil &&
		jobCompleted(upgradeJob)
	upgradeJobFailed := upgradeJob != nil &&
		jobFailed(upgradeJob)

	var removeDataJobsFailed bool
	var removeDataJobsCompleted []*batchv1.Job
	for _, job := range world.Jobs {
		if job.GetLabels()[LabelRole] == removeData {
			if jobCompleted(job) {
				removeDataJobsCompleted = append(removeDataJobsCompleted, job)
			} else if jobFailed(job) {
				removeDataJobsFailed = true
				break
			}
		}
	}
	removeDataJobsComplete := len(removeDataJobsCompleted) == world.ReplicasExpected

	// If the IvoryCluster is already set to the desired version, but the upgradejob has
	// not completed successfully, the operator assumes that the cluster is already
	// running the desired version. We consider this a no-op rather than a successful upgrade.
	// Documentation should make it clear that the IvoryCluster ivoryVersion
	// should be updated _after_ the upgrade is considered successful.
	if version == int64(upgrade.Spec.ToIvoryVersion) && !upgradeJobComplete {
		meta.SetStatusCondition(&upgrade.Status.Conditions, metav1.Condition{
			ObservedGeneration: upgrade.Generation,
			Type:               ConditionIvyUpgradeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "IvyUpgradeResolved",
			Message: fmt.Sprintf(
				"IvoryCluster %s is already running version %d",
				upgrade.Spec.IvoryClusterName, upgrade.Spec.ToIvoryVersion),
		})

		return ctrl.Result{}, nil
	}

	// This condition is unlikely to ever need to be changed, but is added just in case.
	setStatusToProgressingIfReasonWas("IvyUpgradeResolved", upgrade)

	if statusVersion == int64(upgrade.Spec.ToIvoryVersion) {
		meta.SetStatusCondition(&upgrade.Status.Conditions, metav1.Condition{
			ObservedGeneration: upgrade.Generation,
			Type:               ConditionIvyUpgradeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "IvyUpgradeCompleted",
			Message: fmt.Sprintf(
				"IvoryCluster %s is running version %d",
				upgrade.Spec.IvoryClusterName, upgrade.Spec.ToIvoryVersion),
		})

		if upgradeJobComplete && removeDataJobsComplete {
			meta.SetStatusCondition(&upgrade.Status.Conditions, metav1.Condition{
				ObservedGeneration: upgrade.Generation,
				Type:               ConditionIvyUpgradeSucceeded,
				Status:             metav1.ConditionTrue,
				Reason:             "IvyUpgradeSucceeded",
				Message: fmt.Sprintf(
					"IvoryCluster %s is ready to complete upgrade to version %d",
					upgrade.Spec.IvoryClusterName, upgrade.Spec.ToIvoryVersion),
			})
		}

		return ctrl.Result{}, nil
	}

	// The upgrade needs to manipulate the data directory of the primary while
	// Ivory is stopped. Wait until all instances are gone and the primary
	// is identified.
	//
	// Requiring the cluster be shutdown also provides some assurance that the
	// user understands downtime requirement of upgrading
	if !world.ClusterShutdown || world.ClusterPrimary == nil {
		meta.SetStatusCondition(&upgrade.Status.Conditions, metav1.Condition{
			ObservedGeneration: upgrade.Generation,
			Type:               ConditionIvyUpgradeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "PGClusterNotShutdown",
			Message:            "IvoryCluster instances still running",
		})

		return ctrl.Result{}, nil
	}

	setStatusToProgressingIfReasonWas("PGClusterNotShutdown", upgrade)

	if version != int64(upgrade.Spec.FromIvoryVersion) &&
		statusVersion != int64(upgrade.Spec.ToIvoryVersion) {
		meta.SetStatusCondition(&upgrade.Status.Conditions, metav1.Condition{
			ObservedGeneration: upgrade.Generation,
			Type:               ConditionIvyUpgradeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "IvyUpgradeInvalidForCluster",
			Message: fmt.Sprintf(
				"Current ivory version is %d, but upgrade expected %d",
				version, upgrade.Spec.FromIvoryVersion),
		})

		return ctrl.Result{}, nil
	}

	setStatusToProgressingIfReasonWas("IvyUpgradeInvalidForCluster", upgrade)

	// Each upgrade can specify one cluster, but we also want to ensure that
	// each cluster is managed by at most one upgrade. Check that the specified
	// cluster is annotated with the name of *this* upgrade.
	//
	// Having an annotation on the cluster also provides some assurance that
	// the user that created the upgrade also has authority to create or edit
	// the cluster.

	if allowed := world.Cluster.GetAnnotations()[AnnotationAllowUpgrade] == upgrade.Name; !allowed {
		meta.SetStatusCondition(&upgrade.Status.Conditions, metav1.Condition{
			ObservedGeneration: upgrade.Generation,
			Type:               ConditionIvyUpgradeProgressing,
			Status:             metav1.ConditionFalse,
			Reason:             "PGClusterMissingRequiredAnnotation",
			Message: fmt.Sprintf(
				"IvoryCluster %s lacks annotation for upgrade %s",
				upgrade.Spec.IvoryClusterName, upgrade.GetName()),
		})

		return ctrl.Result{}, nil
	}

	setStatusToProgressingIfReasonWas("PGClusterMissingRequiredAnnotation", upgrade)

	// Currently our jobs are set to only run once, so if any job has failed, the
	// upgrade has failed.
	if upgradeJobFailed || removeDataJobsFailed {
		meta.SetStatusCondition(&upgrade.Status.Conditions, metav1.Condition{
			ObservedGeneration: upgrade.Generation,
			Type:               ConditionIvyUpgradeSucceeded,
			Status:             metav1.ConditionFalse,
			Reason:             "IvyUpgradeFailed",
			Message:            "Upgrade jobs failed, please check individual pod logs",
		})

		return ctrl.Result{}, nil
	}

	// If we have reached this point, all preconditions for upgrade are satisfied.
	// If the jobs have already run to completion
	// - delete the replica-create jobs to kick off a backup
	// - delete the IvoryCluster.Status.Repos to kick off a reconcile
	if upgradeJobComplete && removeDataJobsComplete &&
		statusVersion != int64(upgrade.Spec.ToIvoryVersion) {

		// Patroni will try to recreate replicas using pgBackRest. Convince IVO to
		// take a recent backup by deleting its "replica-create" jobs.
		for _, object := range world.Jobs {
			if backup := object.Labels[LabelPGBackRestBackup]; err == nil &&
				backup == ReplicaCreate {

				uid := object.GetUID()
				version := object.GetResourceVersion()
				exactly := client.Preconditions{UID: &uid, ResourceVersion: &version}
				// Jobs default to an `orphanDependents` policy, orphaning pods after deletion.
				// We don't want that, so we set the delete policy explicitly.
				// - https://kubernetes.io/docs/concepts/workloads/controllers/job/
				// - https://github.com/kubernetes/kubernetes/blob/master/pkg/registry/batch/job/strategy.go#L58
				propagate := client.PropagationPolicy(metav1.DeletePropagationBackground)
				err = client.IgnoreNotFound(r.Client.Delete(ctx, object, exactly, propagate))
			}
		}

		if err == nil {
			patch := world.Cluster.DeepCopy()

			// Set the cluster status when we know the upgrade has completed successfully.
			// This will serve to help the user see that the upgrade has completed if they
			// are only watching the IvoryCluster
			patch.Status.PostgresVersion = upgrade.Spec.ToIvoryVersion

			// Set the pgBackRest status for bootstrapping
			patch.Status.PGBackRest.Repos = []v1beta1.RepoStatus{}

			err = r.Status().Patch(ctx, patch, client.MergeFrom(world.Cluster), r.Owner)
		}

		return ctrl.Result{}, err
	}

	// TODO: error from apply could mean that the job exists with a different spec.
	if err == nil && !upgradeJobComplete {
		err = errors.WithStack(r.apply(ctx,
			r.generateUpgradeJob(ctx, upgrade, world.ClusterPrimary)))
	}

	// Create the jobs to remove the data from the replicas, as long as
	// the upgrade job has completed.
	// (When the cluster is not shutdown, the `world.ClusterReplicas` will be [],
	// so there should be no danger of accidentally targeting the primary.)
	if err == nil && upgradeJobComplete && !removeDataJobsComplete {
		for _, sts := range world.ClusterReplicas {
			if err == nil {
				err = r.apply(ctx, r.generateRemoveDataJob(ctx, upgrade, sts))
			}
		}
	}

	// The upgrade job generates a new system identifier for this cluster.
	// Clear the old identifier from Patroni by deleting its DCS Endpoints.
	// This is safe to do this when all Patroni processes are stopped
	// (ClusterShutdown) and IVO has identified a leader to start first
	// (ClusterPrimary).
	// - https://github.com/zalando/patroni/blob/v2.1.2/docs/existing_data.rst
	//
	// TODO(cbandy): This works only when using Kubernetes Endpoints for DCS.
	if len(world.PatroniEndpoints) > 0 {
		for _, object := range world.PatroniEndpoints {
			uid := object.GetUID()
			version := object.GetResourceVersion()
			exactly := client.Preconditions{UID: &uid, ResourceVersion: &version}
			err = client.IgnoreNotFound(r.Client.Delete(ctx, object, exactly))
		}

		// Requeue to verify that Patroni endpoints are deleted
		return ctrl.Result{Requeue: true}, err // FIXME
	}

	// TODO: write upgradeJob back to world? No, we will wake and see it when it
	// has some progress. OTOH, whatever we just wrote has the latest metadata.generation.
	// TODO: consider what it means to "re-use" the same IvyUpgrade for more than
	// one ivory version. Should the job name include the version number?

	log.Info("Reconciled", "requeue", err != nil ||
		result.Requeue ||
		result.RequeueAfter > 0)
	return
}

func setStatusToProgressingIfReasonWas(reason string, upgrade *v1beta1.IvyUpgrade) {
	progressing := meta.FindStatusCondition(upgrade.Status.Conditions,
		ConditionIvyUpgradeProgressing)
	if progressing == nil || (progressing != nil && progressing.Reason == reason) {
		meta.SetStatusCondition(&upgrade.Status.Conditions, metav1.Condition{
			ObservedGeneration: upgrade.GetGeneration(),
			Type:               ConditionIvyUpgradeProgressing,
			Status:             metav1.ConditionTrue,
			Reason:             "IvyUpgradeProgressing",
			Message: fmt.Sprintf(
				"Upgrade progressing for cluster %s",
				upgrade.Spec.IvoryClusterName),
		})
	}
}
