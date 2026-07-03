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
	"crypto/sha256"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// namedPort is a container port with its Service/probe name.
type namedPort struct {
	name string
	port int32
}

// apiPorts are the client-facing API ports served by every node.
func apiPorts() []namedPort {
	return []namedPort{
		{"otlp-grpc", portOTLPGRPC},
		{"otlp-http", portOTLPHTTP},
		{"prom-rw", portPromRW},
		{"prom-http", portPromHTTP},
		{"tempo-http", portTempoHTTP},
		{"loki-http", portLokiHTTP},
		{"pyroscope", portPyroscope},
		{"metrics", portSelfMetric},
		{"health-check", portHealth},
	}
}

// buildConfigMap renders the shared oteldb config into a ConfigMap.
func buildConfigMap(cr *dbv1alpha1.OtelDBCluster, etcdEndpoints []string) (*corev1.ConfigMap, error) {
	data, err := renderConfig(cr, etcdEndpoints)
	if err != nil {
		return nil, err
	}
	n := namesFor(cr)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.configMap(),
			Namespace: cr.Namespace,
			Labels:    commonLabels(cr),
		},
		Data: map[string]string{configFileName: data},
	}, nil
}

// buildPeerService is the headless Service giving each pod a stable DNS name for peer-to-peer
// replication and the etcd-advertised address.
func buildPeerService(cr *dbv1alpha1.OtelDBCluster) *corev1.Service {
	n := namesFor(cr)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.peerService(),
			Namespace: cr.Namespace,
			Labels:    commonLabels(cr),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP:                corev1.ClusterIPNone,
			PublishNotReadyAddresses: true, // peers must resolve each other before readiness
			Selector:                 selectorLabels(cr),
			Ports: []corev1.ServicePort{{
				Name:       "peer",
				Port:       peerPortOf(cr),
				TargetPort: intstr.FromInt32(peerPortOf(cr)),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// buildClientService exposes the query/ingest APIs across all nodes.
func buildClientService(cr *dbv1alpha1.OtelDBCluster) *corev1.Service {
	n := namesFor(cr)
	svcType := cr.Spec.Service.Type
	if svcType == "" {
		svcType = corev1.ServiceTypeClusterIP
	}
	ports := make([]corev1.ServicePort, 0, len(apiPorts()))
	for _, p := range apiPorts() {
		ports = append(ports, corev1.ServicePort{
			Name:       p.name,
			Port:       p.port,
			TargetPort: intstr.FromInt32(p.port),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        n.clientService(),
			Namespace:   cr.Namespace,
			Labels:      commonLabels(cr),
			Annotations: cr.Spec.Service.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: selectorLabels(cr),
			Ports:    ports,
		},
	}
}

// buildStatefulSet builds the oteldb StatefulSet: one symmetric pod per node with a stable
// identity, a per-pod data volume, and per-pod cluster env.
func buildStatefulSet(cr *dbv1alpha1.OtelDBCluster, configHash string) *appsv1.StatefulSet {
	n := namesFor(cr)
	replicas := replicasOf(cr)

	image := cr.Spec.Image
	if image == "" {
		image = defaultImage
	}

	podLabels := mergeLabels(commonLabels(cr), cr.Spec.PodLabels)
	annotations := mergeLabels(map[string]string{"oteldb.io/config-hash": configHash}, cr.Spec.PodAnnotations)

	container := corev1.Container{
		Name:            "oteldb",
		Image:           image,
		ImagePullPolicy: cr.Spec.ImagePullPolicy,
		Args:            []string{"--config=" + configMountPath + "/" + configFileName},
		Env:             podEnv(cr),
		Ports:           containerPorts(cr),
		VolumeMounts: []corev1.VolumeMount{
			{Name: configVolumeName, MountPath: configMountPath},
			{Name: dataVolumeName, MountPath: dirOf(cr)},
		},
		Resources:       cr.Spec.Resources,
		SecurityContext: cr.Spec.SecurityContext,
		LivenessProbe:   httpProbe("/liveness"),
		ReadinessProbe:  httpProbe("/readiness"),
		StartupProbe:    startupProbe("/startup"),
	}

	podSpec := corev1.PodSpec{
		ServiceAccountName:        cr.Spec.ServiceAccountName,
		ImagePullSecrets:          cr.Spec.ImagePullSecrets,
		SecurityContext:           cr.Spec.PodSecurityContext,
		NodeSelector:              cr.Spec.NodeSelector,
		Affinity:                  affinityFor(cr),
		Tolerations:               cr.Spec.Tolerations,
		TopologySpreadConstraints: cr.Spec.TopologySpreadConstraints,
		Containers:                []corev1.Container{container},
		Volumes: []corev1.Volume{{
			Name: configVolumeName,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: n.configMap()},
				},
			},
		}},
	}

	return &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.statefulSet(),
			Namespace: cr.Namespace,
			Labels:    commonLabels(cr),
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         n.peerService(),
			Replicas:            ptr.To(replicas),
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Selector:            &metav1.LabelSelector{MatchLabels: selectorLabels(cr)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      podLabels,
					Annotations: annotations,
				},
				Spec: podSpec,
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{dataClaim(cr)},
		},
	}
}

// podEnv builds the per-pod environment: self-observability plus the per-pod cluster identity
// (id = pod name, addr = pod FQDN via the headless service). Kubernetes expands $(POD_NAME).
func podEnv(cr *dbv1alpha1.OtelDBCluster) []corev1.EnvVar {
	n := namesFor(cr)
	fqdnSuffix := fmt.Sprintf(".%s.%s.svc.cluster.local:%d", n.peerService(), cr.Namespace, peerPortOf(cr))
	env := []corev1.EnvVar{
		{Name: "OTEL_EXPORTER_PROMETHEUS_HOST", Value: "0.0.0.0"},
		{Name: "OTEL_EXPORTER_PROMETHEUS_PORT", Value: fmt.Sprintf("%d", portSelfMetric)},
		{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{
			FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"},
		}},
		{Name: "OTELDB_CLUSTER_ID", Value: "$(POD_NAME)"},
		{Name: "OTELDB_CLUSTER_ADDR", Value: "$(POD_NAME)" + fqdnSuffix},
	}
	if lvl := cr.Spec.LogLevel; lvl != "" {
		env = append(env, corev1.EnvVar{Name: "OTEL_LOG_LEVEL", Value: lvl})
	}
	if z := cr.Spec.Cluster.StaticZone; z != "" {
		env = append(env, corev1.EnvVar{Name: "OTELDB_CLUSTER_ZONE", Value: z})
	}
	// S3 static credentials via the AWS default chain, sourced from a Secret (never the config).
	if backendOf(cr) == dbv1alpha1.StorageBackendS3 && cr.Spec.Storage.S3 != nil && cr.Spec.Storage.S3.CredentialsSecret != nil {
		cs := cr.Spec.Storage.S3.CredentialsSecret
		akKey := cs.AccessKeyIDKey
		if akKey == "" {
			akKey = "AWS_ACCESS_KEY_ID"
		}
		skKey := cs.SecretAccessKeyKey
		if skKey == "" {
			skKey = "AWS_SECRET_ACCESS_KEY"
		}
		env = append(env,
			corev1.EnvVar{Name: "AWS_ACCESS_KEY_ID", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: cs.Name},
					Key:                  akKey,
				},
			}},
			corev1.EnvVar{Name: "AWS_SECRET_ACCESS_KEY", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: cs.Name},
					Key:                  skKey,
				},
			}},
		)
	}
	return env
}

func containerPorts(cr *dbv1alpha1.OtelDBCluster) []corev1.ContainerPort {
	ports := make([]corev1.ContainerPort, 0, len(apiPorts())+1)
	for _, p := range apiPorts() {
		ports = append(ports, corev1.ContainerPort{Name: p.name, ContainerPort: p.port, Protocol: corev1.ProtocolTCP})
	}
	ports = append(ports, corev1.ContainerPort{Name: "peer", ContainerPort: peerPortOf(cr), Protocol: corev1.ProtocolTCP})
	return ports
}

func httpProbe(path string) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromString("health-check")},
		},
		PeriodSeconds:    10,
		TimeoutSeconds:   3,
		FailureThreshold: 3,
	}
}

func startupProbe(path string) *corev1.Probe {
	p := httpProbe(path)
	p.FailureThreshold = 30 // allow up to ~5m for recovery/replay on startup
	p.PeriodSeconds = 10
	return p
}

// dataClaim is the per-pod data volume (the file backend's parts + WAL, or the s3 WAL).
func dataClaim(cr *dbv1alpha1.OtelDBCluster) corev1.PersistentVolumeClaim {
	size := cr.Spec.Storage.Size
	if size.IsZero() {
		size = resource.MustParse("50Gi")
	}
	accessModes := cr.Spec.Storage.AccessModes
	if len(accessModes) == 0 {
		accessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}
	return corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: dataVolumeName, Labels: commonLabels(cr)},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      accessModes,
			StorageClassName: cr.Spec.Storage.StorageClassName,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: size},
			},
		},
	}
}

// affinityFor returns the user's affinity, or a default soft anti-affinity spreading replicas
// across nodes.
func affinityFor(cr *dbv1alpha1.OtelDBCluster) *corev1.Affinity {
	if cr.Spec.Affinity != nil {
		return cr.Spec.Affinity
	}
	return &corev1.Affinity{
		PodAntiAffinity: &corev1.PodAntiAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
				Weight: 100,
				PodAffinityTerm: corev1.PodAffinityTerm{
					TopologyKey:   "kubernetes.io/hostname",
					LabelSelector: &metav1.LabelSelector{MatchLabels: selectorLabels(cr)},
				},
			}},
		},
	}
}

// configHash is a short digest of the rendered config, used to trigger a rollout on change.
func configHash(config string) string {
	sum := sha256.Sum256([]byte(config))
	return fmt.Sprintf("%x", sum)[:16]
}
