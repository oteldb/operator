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
	keyTenant:  "use spec.ingest.tenant",
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
	if err := validateIngestTenant(cr); err != nil {
		return "", err
	}

	cfg := map[string]any{
		keyCluster: ringConfig(cr, etcdEndpoints),
		"prometheus_remote_write": map[string]any{
			keyBind: bindAll(portPromRW),
		},
		"otlp": map[string]any{
			"grpc_bind": bindAll(portOTLPGRPC),
		},
	}

	if tenant := renderTenant(cr.Spec.Ingest.Tenant); tenant != nil {
		cfg[keyTenant] = tenant
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

// renderTenant builds odbingest's tenant block, or nil when the spec configures nothing. odbingest
// installs no resolver for an empty block, which is the unconfigured behaviour: everything routes
// to "default".
func renderTenant(spec *dbv1alpha1.TenantSpec) map[string]any {
	if spec == nil {
		return nil
	}

	tenant := map[string]any{}
	if spec.Header != "" {
		tenant[keyTenantHeader] = spec.Header
	}
	if len(spec.ResourceAttributes) > 0 {
		attrs := make([]any, 0, len(spec.ResourceAttributes))
		for _, k := range spec.ResourceAttributes {
			attrs = append(attrs, k)
		}
		tenant[keyTenantResourceAttr] = attrs
	}
	if spec.Default != "" {
		tenant[keyTenantDefault] = spec.Default
	}
	// require is only meaningful alongside a header, which validateIngestTenant enforces, so an
	// unset one is left out rather than rendered as an explicit false.
	if spec.Require {
		tenant[keyTenantRequire] = true
	}

	if len(tenant) == 0 {
		return nil
	}
	return tenant
}

// maxTenantLen bounds a tenant id, mirroring cmd/odbingest. A tenant id becomes a shard key, which
// becomes a backend path segment and an etcd key component.
const maxTenantLen = 150

// validateIngestTenant rejects a tenant block odbingest would refuse at startup, or that would
// resolve to nothing. Every check mirrors cmd/odbingest's newTenantResolver and parseTenantID: a
// pod that crash-loops on its config is a worse report than a Degraded condition naming the field.
func validateIngestTenant(cr *dbv1alpha1.OtelDBCluster) error {
	if !ingestEnabled(cr) {
		return nil
	}
	spec := cr.Spec.Ingest.Tenant
	if spec == nil {
		return nil
	}

	if spec.Require && spec.Header == "" {
		return invalidSpec("spec.ingest.tenant.require needs spec.ingest.tenant.header: " +
			"there is no header to require")
	}
	for i, key := range spec.ResourceAttributes {
		if key == "" {
			return invalidSpec("spec.ingest.tenant.resourceAttributes[%d] must not be empty", i)
		}
	}
	if spec.Default != "" {
		if err := validateTenantID(spec.Default); err != nil {
			return invalidSpec("spec.ingest.tenant.default: %s", err)
		}
	}

	// A block that names no source resolves nothing: odbingest builds no resolver and routes to
	// "default", so the stanza is present and inert.
	if spec.Header == "" && spec.Default == "" && len(spec.ResourceAttributes) == 0 {
		return invalidSpec("spec.ingest.tenant sets no source: give it a header, " +
			"resourceAttributes or a default, or remove the block")
	}

	return nil
}

// validateTenantID mirrors cmd/odbingest's parseTenantID. The character set is what stays safe as a
// backend path segment and an etcd key.
func validateTenantID(s string) error {
	switch {
	case len(s) > maxTenantLen:
		return fmt.Errorf("tenant id longer than %d bytes", maxTenantLen)
	case s == ".", s == "..":
		return fmt.Errorf("invalid tenant id %q", s)
	}
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
		default:
			return fmt.Errorf("invalid character %q in tenant id", string(c))
		}
	}
	return nil
}
