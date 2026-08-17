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
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// ingestReservedConfigPaths are the odbingest config paths the operator owns. The ring parameters
// must stay in step with the storage nodes, and a mismatch is silent: it resolves a different owner
// set, so writes land where no read will look for them.
var ingestReservedConfigPaths = map[string]string{
	keyCluster: hintRingFromSpec,
}

func ingestEnabled(cr *dbv1alpha1.OtelDBCluster) bool { return cr.Spec.Ingest != nil }

func ingestReplicasOf(cr *dbv1alpha1.OtelDBCluster) int32 {
	if !ingestEnabled(cr) {
		return 0
	}
	if r := cr.Spec.Ingest.Replicas; r != nil {
		return *r
	}
	return 2
}

// renderIngestConfig builds odbingest.yml. odbingest holds no data and joins nothing, so the whole
// config is the ring it routes into plus the two listeners it serves.
func renderIngestConfig(cr *dbv1alpha1.OtelDBCluster, etcdEndpoints []string) (string, error) {
	cfg := map[string]any{
		keyCluster: ringConfig(cr, etcdEndpoints),
		"prometheus_remote_write": map[string]any{
			keyBind: bindAll(portPromRW),
		},
		"otlp": map[string]any{
			"grpc_bind": bindAll(portOTLPGRPC),
		},
	}

	if raw := cr.Spec.Ingest.ExtraConfig; raw != nil && len(raw.Raw) > 0 {
		var extra map[string]any
		if err := json.Unmarshal(raw.Raw, &extra); err != nil {
			return "", invalidSpec("parse spec.ingest.extraConfig: %s", err)
		}
		if err := validateExtraConfigPaths("spec.ingest.extraConfig", extra, ingestReservedConfigPaths); err != nil {
			return "", err
		}
		deepMerge(cfg, extra)
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal ingest config: %w", err)
	}
	return string(out), nil
}

// buildIngestConfigMap renders the shared odbingest config into a ConfigMap.
func buildIngestConfigMap(cr *dbv1alpha1.OtelDBCluster, etcdEndpoints []string) (*corev1.ConfigMap, error) {
	data, err := renderIngestConfig(cr, etcdEndpoints)
	if err != nil {
		return nil, err
	}
	n := namesFor(cr)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.ingestConfigMap(),
			Namespace: cr.Namespace,
			Labels:    ingestCommonLabels(cr),
		},
		Data: map[string]string{ingestConfigFileName: data},
	}, nil
}

// buildIngestService exposes the ingest endpoints across the odbingest pool. This is where OTLP
// exporters and remote write senders point; the cluster's client Service keeps fronting the
// storage nodes.
func buildIngestService(cr *dbv1alpha1.OtelDBCluster) *corev1.Service {
	n := namesFor(cr)
	svcType := cr.Spec.Ingest.Service.Type
	if svcType == "" {
		svcType = corev1.ServiceTypeClusterIP
	}

	// OTLP/HTTP is published on 4318, the port every OTLP/HTTP exporter targets by default, but
	// odbingest serves it on the remote write listener, so it targets that container port.
	ports := []corev1.ServicePort{
		{
			Name:       portNameOTLPGRPC,
			Port:       portOTLPGRPC,
			TargetPort: intstr.FromString(portNameOTLPGRPC),
			Protocol:   corev1.ProtocolTCP,
		},
		{
			Name:       portNameOTLPHTTP,
			Port:       portOTLPHTTP,
			TargetPort: intstr.FromString(portNameIngestHTTP),
			Protocol:   corev1.ProtocolTCP,
		},
	}
	if metricsEnabled(cr) {
		ports = append(ports, corev1.ServicePort{
			Name:       portNamePromRW,
			Port:       portPromRW,
			TargetPort: intstr.FromString(portNameIngestHTTP),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	ports = append(ports, corev1.ServicePort{
		Name:       portNameSelfMetric,
		Port:       portSelfMetric,
		TargetPort: intstr.FromString(portNameSelfMetric),
		Protocol:   corev1.ProtocolTCP,
	})

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        n.ingestService(),
			Namespace:   cr.Namespace,
			Labels:      ingestCommonLabels(cr),
			Annotations: cr.Spec.Ingest.Service.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: ingestSelectorLabels(cr),
			Ports:    ports,
		},
	}
}

// buildIngestDeployment builds the odbingest pool. It is a Deployment, not a StatefulSet: the pods
// hold no data, are not ring members, and need neither a stable identity nor a volume.
func buildIngestDeployment(cr *dbv1alpha1.OtelDBCluster, configHash string) *appsv1.Deployment {
	n := namesFor(cr)
	spec := cr.Spec.Ingest

	image := spec.Image
	if image == "" {
		image = imageOf(cr)
	}

	serviceAccount := spec.ServiceAccountName
	if serviceAccount == "" {
		serviceAccount = cr.Spec.ServiceAccountName
	}

	podLabels := mergeLabels(ingestCommonLabels(cr), spec.PodLabels)
	annotations := mergeLabels(map[string]string{annConfigHash: configHash}, spec.PodAnnotations)

	container := corev1.Container{
		Name:            ingestAppName,
		Image:           image,
		ImagePullPolicy: cr.Spec.ImagePullPolicy,
		Command:         []string{ingestBinPath},
		Args:            []string{"--config=" + configMountPath + "/" + ingestConfigFileName},
		Env:             statelessPodEnv(cr),
		Ports: []corev1.ContainerPort{
			{Name: portNameOTLPGRPC, ContainerPort: portOTLPGRPC, Protocol: corev1.ProtocolTCP},
			{Name: portNameIngestHTTP, ContainerPort: portPromRW, Protocol: corev1.ProtocolTCP},
			{Name: portNameSelfMetric, ContainerPort: portSelfMetric, Protocol: corev1.ProtocolTCP},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: configVolumeName, MountPath: configMountPath},
		},
		Resources:       spec.Resources,
		SecurityContext: spec.SecurityContext,
		LivenessProbe:   ingestProbe("/healthz"),
		// /readyz answers 503 until the ring has a member, keeping a starting pod out of the load
		// balancer while a write against an empty ring could only fail.
		ReadinessProbe: ingestProbe("/readyz"),
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.ingestDeployment(),
			Namespace: cr.Namespace,
			Labels:    ingestCommonLabels(cr),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(ingestReplicasOf(cr)),
			Selector: &metav1.LabelSelector{MatchLabels: ingestSelectorLabels(cr)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName:        serviceAccount,
					ImagePullSecrets:          cr.Spec.ImagePullSecrets,
					SecurityContext:           spec.PodSecurityContext,
					NodeSelector:              spec.NodeSelector,
					Affinity:                  affinityOr(spec.Affinity, ingestSelectorLabels(cr)),
					Tolerations:               spec.Tolerations,
					TopologySpreadConstraints: spec.TopologySpreadConstraints,
					Containers:                []corev1.Container{container},
					Volumes: []corev1.Volume{{
						Name: configVolumeName,
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: n.ingestConfigMap()},
							},
						},
					}},
				},
			},
		},
	}
}

func ingestProbe(path string) *corev1.Probe { return roleProbe(path, portNameIngestHTTP) }
