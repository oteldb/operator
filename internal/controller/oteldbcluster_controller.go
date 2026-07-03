/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// OtelDBClusterReconciler reconciles a OtelDBCluster object
type OtelDBClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=db.oteldb.io,resources=oteldbclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=db.oteldb.io,resources=oteldbclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=db.oteldb.io,resources=oteldbclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives the actual cluster state towards the OtelDBCluster spec.
func (r *OtelDBClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	cr := &dbv1alpha1.OtelDBCluster{}
	if err := r.Get(ctx, req.NamespacedName, cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !cr.DeletionTimestamp.IsZero() {
		// Owned objects are garbage-collected via owner references; nothing to do.
		return ctrl.Result{}, nil
	}

	if err := r.reconcile(ctx, cr); err != nil {
		log.Error(err, "reconcile failed")
		r.setDegraded(ctx, cr, err)
		return ctrl.Result{}, err
	}

	if err := r.updateStatus(ctx, cr); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *OtelDBClusterReconciler) reconcile(ctx context.Context, cr *dbv1alpha1.OtelDBCluster) error {
	endpoints := cr.Spec.Etcd.Endpoints
	if len(endpoints) == 0 {
		return fmt.Errorf("spec.etcd.endpoints is required (bring your own etcd)")
	}

	cm, err := buildConfigMap(cr, endpoints)
	if err != nil {
		return err
	}
	if err := r.apply(ctx, cr, cm); err != nil {
		return fmt.Errorf("configmap: %w", err)
	}
	hash := configHash(cm.Data[configFileName])

	if err := r.apply(ctx, cr, buildPeerService(cr)); err != nil {
		return fmt.Errorf("peer service: %w", err)
	}
	if err := r.apply(ctx, cr, buildClientService(cr)); err != nil {
		return fmt.Errorf("client service: %w", err)
	}
	if err := r.apply(ctx, cr, buildStatefulSet(cr, hash)); err != nil {
		return fmt.Errorf("statefulset: %w", err)
	}
	return nil
}

// apply creates or updates obj as a child of cr, preserving server-managed fields.
func (r *OtelDBClusterReconciler) apply(ctx context.Context, cr *dbv1alpha1.OtelDBCluster, obj client.Object) error {
	// Snapshot the desired spec so we can restore it after CreateOrUpdate fetches the live object.
	desired := obj.DeepCopyObject().(client.Object)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		reconcileInto(obj, desired)
		return controllerutil.SetControllerReference(cr, obj, r.Scheme)
	})
	return err
}

// reconcileInto copies the desired, operator-owned fields from src onto dst (which holds the live
// object's server-managed metadata like resourceVersion and clusterIP).
func reconcileInto(dst, src client.Object) {
	// Labels/annotations: desired wins but keep any extra live ones.
	dst.SetLabels(mergeLabels(dst.GetLabels(), src.GetLabels()))
	dst.SetAnnotations(mergeLabels(dst.GetAnnotations(), src.GetAnnotations()))

	switch d := dst.(type) {
	case *corev1.ConfigMap:
		d.Data = src.(*corev1.ConfigMap).Data
	case *corev1.Service:
		s := src.(*corev1.Service)
		// Preserve the allocated ClusterIP(s) on update.
		clusterIP := d.Spec.ClusterIP
		clusterIPs := d.Spec.ClusterIPs
		d.Spec.Ports = s.Spec.Ports
		d.Spec.Selector = s.Spec.Selector
		d.Spec.Type = s.Spec.Type
		d.Spec.PublishNotReadyAddresses = s.Spec.PublishNotReadyAddresses
		if s.Spec.ClusterIP == corev1.ClusterIPNone {
			d.Spec.ClusterIP = corev1.ClusterIPNone
		} else if clusterIP != "" {
			d.Spec.ClusterIP = clusterIP
			d.Spec.ClusterIPs = clusterIPs
		}
	case *appsv1.StatefulSet:
		s := src.(*appsv1.StatefulSet)
		// VolumeClaimTemplates are immutable after creation; only set them on create (empty live).
		if len(d.Spec.VolumeClaimTemplates) == 0 {
			d.Spec.VolumeClaimTemplates = s.Spec.VolumeClaimTemplates
		}
		d.Spec.Replicas = s.Spec.Replicas
		d.Spec.Selector = s.Spec.Selector
		d.Spec.ServiceName = s.Spec.ServiceName
		d.Spec.PodManagementPolicy = s.Spec.PodManagementPolicy
		d.Spec.Template = s.Spec.Template
	}
}

func (r *OtelDBClusterReconciler) updateStatus(ctx context.Context, cr *dbv1alpha1.OtelDBCluster) error {
	sts := &appsv1.StatefulSet{}
	desired := replicasOf(cr)
	ready := int32(0)
	if err := r.Get(ctx, client.ObjectKey{Namespace: cr.Namespace, Name: namesFor(cr).statefulSet()}, sts); err == nil {
		ready = sts.Status.ReadyReplicas
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	cr.Status.Replicas = desired
	cr.Status.ReadyReplicas = ready
	cr.Status.EtcdEndpoints = cr.Spec.Etcd.Endpoints
	cr.Status.ObservedGeneration = cr.Generation

	switch {
	case ready == 0:
		cr.Status.Phase = dbv1alpha1.PhasePending
	case ready < desired:
		cr.Status.Phase = dbv1alpha1.PhaseProgressing
	default:
		cr.Status.Phase = dbv1alpha1.PhaseReady
	}

	setCondition(cr, dbv1alpha1.ConditionAvailable, boolStatus(ready > 0), "Nodes",
		fmt.Sprintf("%d/%d nodes ready", ready, desired))
	setCondition(cr, dbv1alpha1.ConditionProgressing, boolStatus(ready < desired), "Rollout",
		fmt.Sprintf("%d/%d nodes ready", ready, desired))
	setCondition(cr, dbv1alpha1.ConditionDegraded, metav1.ConditionFalse, "Reconciled", "reconciled successfully")

	return r.Status().Update(ctx, cr)
}

func (r *OtelDBClusterReconciler) setDegraded(ctx context.Context, cr *dbv1alpha1.OtelDBCluster, cause error) {
	cr.Status.Phase = dbv1alpha1.PhaseDegraded
	setCondition(cr, dbv1alpha1.ConditionDegraded, metav1.ConditionTrue, "ReconcileError", cause.Error())
	_ = r.Status().Update(ctx, cr)
}

func setCondition(cr *dbv1alpha1.OtelDBCluster, condType string, status metav1.ConditionStatus, reason, msg string) {
	meta := metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            msg,
		ObservedGeneration: cr.Generation,
	}
	apimeta.SetStatusCondition(&cr.Status.Conditions, meta)
}

func boolStatus(b bool) metav1.ConditionStatus {
	if b {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

// SetupWithManager sets up the controller with the Manager.
func (r *OtelDBClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbv1alpha1.OtelDBCluster{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Named("oteldbcluster").
		Complete(r)
}
