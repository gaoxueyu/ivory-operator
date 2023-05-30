package ivorycluster

/*
Copyright 2021 - 2023 Highgo Solutions, Inc.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/highgo/ivory-operator/internal/kubeapi"
	"github.com/highgo/ivory-operator/internal/logging"
	"github.com/highgo/ivory-operator/internal/naming"
	"github.com/highgo/ivory-operator/pkg/apis/ivory-operator.highgo.com/v1beta1"
)

// adoptObject adopts the provided Object by adding controller owner refs for the provided
// IvoryCluster.
func (r *Reconciler) adoptObject(ctx context.Context, ivoryCluster *v1beta1.IvoryCluster,
	obj client.Object) error {

	if err := controllerutil.SetControllerReference(ivoryCluster, obj,
		r.Client.Scheme()); err != nil {
		return err
	}

	patchBytes, err := kubeapi.NewMergePatch().
		Add("metadata", "ownerReferences")(obj.GetOwnerReferences()).Bytes()
	if err != nil {
		return err
	}

	return r.Client.Patch(ctx, obj, client.RawPatch(types.StrategicMergePatchType,
		patchBytes), &client.PatchOptions{
		FieldManager: ControllerName,
	})
}

// claimObject is responsible for adopting or releasing Objects based on their current
// controller ownership and whether or not they meet the provided labeling requirements.
// This solution is modeled after the ControllerRefManager logic as found within the controller
// package in the Kubernetes source:
// https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/controller_ref_manager.go
//
// TODO do a non-cache based get of the IvoryCluster prior to adopting anything to prevent
// race conditions with the garbage collector (see
// https://github.com/kubernetes/kubernetes/issues/42639)
func (r *Reconciler) claimObject(ctx context.Context, ivoryCluster *v1beta1.IvoryCluster,
	obj client.Object) error {

	controllerRef := metav1.GetControllerOfNoCopy(obj)
	if controllerRef != nil {
		// if not owned by this ivorycluster then ignore
		if controllerRef.UID != ivoryCluster.GetUID() {
			return nil
		}

		// If owned by this IvoryCluster and the proper IvoryCluster label is present then
		// return true.  Additional labels checks can be added here as needed to determine whether
		// or not a StatefulSet is part of a IvorySQL cluster and should be adopted or released.
		if _, ok := obj.GetLabels()[naming.LabelCluster]; ok {
			return nil
		}

		// If owned but selector doesn't match, then attempt to release.  However, if the
		// IvoryCluster is being deleted then simply return.
		if ivoryCluster.GetDeletionTimestamp() != nil {
			return nil
		}

		if err := r.releaseObject(ctx, ivoryCluster,
			obj); client.IgnoreNotFound(err) != nil {
			return err
		}

		// successfully released resource or resource no longer exists
		return nil
	}

	// At this point the resource has no controller ref and is therefore an orphan.  Ignore if
	// either the IvoryCluster resource or the orphaned resource is being deleted, or if the selector
	// for the orphaned resource doesn't doesn't include the proper IvoryCluster label
	_, hasIVYClusterLabel := obj.GetLabels()[naming.LabelCluster]
	if ivoryCluster.GetDeletionTimestamp() != nil || !hasIVYClusterLabel {
		return nil
	}
	if obj.GetDeletionTimestamp() != nil {
		return nil
	}
	if err := r.adoptObject(ctx, ivoryCluster, obj); err != nil {
		// If adopt attempt failed because the resource no longer exists, then simply
		// ignore.  Otherwise return the error.
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	// successfully adopted resource
	return nil
}

// getIvoryClusterForObject is responsible for obtaining the IvoryCluster associated
// with an Object.
func (r *Reconciler) getIvoryClusterForObject(ctx context.Context,
	obj client.Object) (bool, *v1beta1.IvoryCluster, error) {

	clusterName := ""

	// first see if it has a IvoryCluster ownership ref or a IvoryCluster label
	controllerRef := metav1.GetControllerOfNoCopy(obj)
	if controllerRef != nil && controllerRef.Kind == "IvoryCluster" {
		clusterName = controllerRef.Name
	} else if _, ok := obj.GetLabels()[naming.LabelCluster]; ok {
		clusterName = obj.GetLabels()[naming.LabelCluster]
	}

	if clusterName == "" {
		return false, nil, nil
	}

	ivoryCluster := &v1beta1.IvoryCluster{}
	if err := r.Client.Get(ctx, types.NamespacedName{
		Name:      clusterName,
		Namespace: obj.GetNamespace(),
	}, ivoryCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil, nil
		}
		return false, nil, err
	}

	return true, ivoryCluster, nil
}

// manageControllerRefs is responsible for determining whether or not an attempt should be made
// to adopt or release/orphan an Object.  This includes obtaining the IvoryCluster for
// the Object and then calling the logic needed to either adopt or release it.
func (r *Reconciler) manageControllerRefs(ctx context.Context,
	obj client.Object) error {

	found, ivoryCluster, err := r.getIvoryClusterForObject(ctx, obj)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	return r.claimObject(ctx, ivoryCluster, obj)
}

// releaseObject releases the provided Object set from ownership by the provided
// IvoryCluster.  This is done by removing the IvoryCluster's controller owner
// refs from the Object.
func (r *Reconciler) releaseObject(ctx context.Context,
	ivoryCluster *v1beta1.IvoryCluster, obj client.Object) error {

	// TODO create a strategic merge type in kubeapi instead of using Merge7386
	patch, err := kubeapi.NewMergePatch().
		Add("metadata", "ownerReferences")([]map[string]string{{
		"$patch": "delete",
		"uid":    string(ivoryCluster.GetUID()),
	}}).Bytes()
	if err != nil {
		return err
	}

	return r.Client.Patch(ctx, obj, client.RawPatch(types.StrategicMergePatchType, patch))
}

// controllerRefHandlerFuncs returns the handler funcs that should be utilized to watch
// StatefulSets within the cluster as needed to manage controller ownership refs.
func (r *Reconciler) controllerRefHandlerFuncs() *handler.Funcs {

	// var err error
	ctx := context.Background()
	log := logging.FromContext(ctx)
	errMsg := "managing StatefulSet controller refs"

	return &handler.Funcs{
		CreateFunc: func(updateEvent event.CreateEvent, workQueue workqueue.RateLimitingInterface) {
			if err := r.manageControllerRefs(ctx, updateEvent.Object); err != nil {
				log.Error(err, errMsg)
			}
		},
		UpdateFunc: func(updateEvent event.UpdateEvent, workQueue workqueue.RateLimitingInterface) {
			if err := r.manageControllerRefs(ctx, updateEvent.ObjectNew); err != nil {
				log.Error(err, errMsg)
			}
		},
		DeleteFunc: func(updateEvent event.DeleteEvent, workQueue workqueue.RateLimitingInterface) {
			if err := r.manageControllerRefs(ctx, updateEvent.Object); err != nil {
				log.Error(err, errMsg)
			}
		},
	}
}
