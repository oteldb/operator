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

// queryReservedConfigPaths are the odbselect config paths the operator owns.
//
// The ring parameters must stay in step with the storage nodes, and a mismatch is silent: it
// resolves a different owner set, so reads look where the data is not.
//
// The listener addresses are reserved for the same reason. odbselect serves each API on its own
// listener and its health endpoints on a fifth, so moving one bind does not stop the process or
// fail a probe — it leaves the Service port the operator published pointing at nothing while the
// pod stays Ready. spec.signals is how an API is turned off.
var queryReservedConfigPaths = map[string]string{
	keyCluster: hintRingFromSpec,

	keyPrometheus + "." + keyBind: "use spec.signals.metrics",
	keyLoki + "." + keyBind:       "use spec.signals.logs",
	keyTempo + "." + keyBind:      "use spec.signals.traces",
	keyPyroscope + "." + keyBind:  "use spec.signals.profiles",
	keyHealth + "." + keyBind:     "the operator probes this listener",
}

func queryEnabled(cr *dbv1alpha1.OtelDBCluster) bool { return cr.Spec.Query != nil }

func queryReplicasOf(cr *dbv1alpha1.OtelDBCluster) int32 {
	if !queryEnabled(cr) {
		return 0
	}
	if r := cr.Spec.Query.Replicas; r != nil {
		return *r
	}
	return 2
}

// queryAPIs are the four query APIs odbselect serves, each on its own listener, paired with the
// signal that decides whether the cluster stores anything for it.
func queryAPIs(cr *dbv1alpha1.OtelDBCluster) []queryAPI {
	return []queryAPI{
		{key: keyPrometheus, port: namedPort{portNamePromHTTP, portPromHTTP}, enabled: metricsEnabled(cr)},
		{key: keyLoki, port: namedPort{portNameLokiHTTP, portLokiHTTP}, enabled: logsEnabled(cr)},
		{key: keyTempo, port: namedPort{portNameTempoHTTP, portTempoHTTP}, enabled: tracesEnabled(cr)},
		{key: keyPyroscope, port: namedPort{portNamePyroscope, portPyroscope}, enabled: profilesEnabled(cr)},
	}
}

// queryAPI is one odbselect query API: its config block, its port, and whether it is served.
type queryAPI struct {
	key     string
	port    namedPort
	enabled bool
}

// renderQueryConfig builds odbselect.yml. odbselect holds no data and joins nothing, so the whole
// config is the ring it reads through plus the listeners it answers on.
func renderQueryConfig(cr *dbv1alpha1.OtelDBCluster, etcdEndpoints []string) (string, error) {
	if err := validateSignals(cr); err != nil {
		return "", err
	}

	cfg := map[string]any{
		keyCluster: ringConfig(cr, etcdEndpoints),
		keyHealth:  map[string]any{keyBind: bindAll(portHealth)},
	}

	// A disabled signal is switched off explicitly rather than by dropping its block: odbselect
	// defaults every API block it does not find, so an omitted one is served on its default port.
	for _, api := range queryAPIs(cr) {
		bind := bindDisabled
		if api.enabled {
			bind = bindAll(api.port.port)
		}
		cfg[api.key] = map[string]any{keyBind: bind}
	}

	if raw := cr.Spec.Query.ExtraConfig; raw != nil && len(raw.Raw) > 0 {
		var extra map[string]any
		if err := json.Unmarshal(raw.Raw, &extra); err != nil {
			return "", invalidSpec("parse spec.query.extraConfig: %s", err)
		}
		if err := validateExtraConfigPaths("spec.query.extraConfig", extra, queryReservedConfigPaths); err != nil {
			return "", err
		}
		deepMerge(cfg, extra)
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal query config: %w", err)
	}
	return string(out), nil
}

// buildQueryConfigMap renders the shared odbselect config into a ConfigMap.
func buildQueryConfigMap(cr *dbv1alpha1.OtelDBCluster, etcdEndpoints []string) (*corev1.ConfigMap, error) {
	data, err := renderQueryConfig(cr, etcdEndpoints)
	if err != nil {
		return nil, err
	}
	n := namesFor(cr)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.queryConfigMap(),
			Namespace: cr.Namespace,
			Labels:    queryCommonLabels(cr),
		},
		Data: map[string]string{queryConfigFileName: data},
	}, nil
}

// buildQueryService exposes every enabled query API across the odbselect pool. This is where
// Grafana points; the cluster's client Service keeps fronting the storage nodes.
//
// One Service carries all four APIs. They are one pod set behind one selector and each API already
// has its own port, so a Service per API would add DNS names and, for a LoadBalancer type, four
// load balancers, without making anything addressable that is not addressable now.
func buildQueryService(cr *dbv1alpha1.OtelDBCluster) *corev1.Service {
	n := namesFor(cr)
	svcType := cr.Spec.Query.Service.Type
	if svcType == "" {
		svcType = corev1.ServiceTypeClusterIP
	}

	ports := make([]corev1.ServicePort, 0, len(queryAPIs(cr))+1)
	for _, p := range queryPorts(cr) {
		ports = append(ports, corev1.ServicePort{
			Name:       p.name,
			Port:       p.port,
			TargetPort: intstr.FromString(p.name),
			Protocol:   corev1.ProtocolTCP,
		})
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        n.queryService(),
			Namespace:   cr.Namespace,
			Labels:      queryCommonLabels(cr),
			Annotations: cr.Spec.Query.Service.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: querySelectorLabels(cr),
			Ports:    ports,
		},
	}
}

// queryPorts are the ports an odbselect pod listens on: one per enabled API plus self-metrics. The
// health listener is deliberately not published — it is probed, not served to clients.
func queryPorts(cr *dbv1alpha1.OtelDBCluster) []namedPort {
	apis := queryAPIs(cr)
	ports := make([]namedPort, 0, len(apis)+1)
	for _, api := range apis {
		if api.enabled {
			ports = append(ports, api.port)
		}
	}
	return append(ports, namedPort{portNameSelfMetric, portSelfMetric})
}

// buildQueryDeployment builds the odbselect pool. It is a Deployment, not a StatefulSet: the pods
// hold no data, are not ring members, and need neither a stable identity nor a volume.
func buildQueryDeployment(cr *dbv1alpha1.OtelDBCluster, configHash string) *appsv1.Deployment {
	n := namesFor(cr)
	spec := cr.Spec.Query

	image := spec.Image
	if image == "" {
		image = imageOf(cr)
	}

	serviceAccount := spec.ServiceAccountName
	if serviceAccount == "" {
		serviceAccount = cr.Spec.ServiceAccountName
	}

	podLabels := mergeLabels(queryCommonLabels(cr), spec.PodLabels)
	annotations := mergeLabels(map[string]string{annConfigHash: configHash}, spec.PodAnnotations)

	ports := make([]corev1.ContainerPort, 0, len(queryPorts(cr))+1)
	for _, p := range queryPorts(cr) {
		ports = append(ports, corev1.ContainerPort{Name: p.name, ContainerPort: p.port, Protocol: corev1.ProtocolTCP})
	}
	ports = append(ports, corev1.ContainerPort{Name: portNameHealth, ContainerPort: portHealth, Protocol: corev1.ProtocolTCP})

	container := corev1.Container{
		Name:            queryAppName,
		Image:           image,
		ImagePullPolicy: cr.Spec.ImagePullPolicy,
		Command:         []string{queryBinPath},
		Args:            []string{"--config=" + configMountPath + "/" + queryConfigFileName},
		Env:             statelessPodEnv(cr),
		Ports:           ports,
		VolumeMounts: []corev1.VolumeMount{
			{Name: configVolumeName, MountPath: configMountPath},
		},
		Resources:       spec.Resources,
		SecurityContext: spec.SecurityContext,
		// The probes hit the health listener, not a query API: it is the one listener odbselect
		// always serves, whatever spec.signals leaves enabled.
		LivenessProbe: roleProbe("/healthz", portNameHealth),
		// /readyz answers 503 until the ring has a member, keeping a starting pod out of the load
		// balancer while a query could only return an empty result that looks like an answer.
		ReadinessProbe: roleProbe("/readyz", portNameHealth),
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.queryDeployment(),
			Namespace: cr.Namespace,
			Labels:    queryCommonLabels(cr),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(queryReplicasOf(cr)),
			Selector: &metav1.LabelSelector{MatchLabels: querySelectorLabels(cr)},
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
					Affinity:                  affinityOr(spec.Affinity, querySelectorLabels(cr)),
					Tolerations:               spec.Tolerations,
					TopologySpreadConstraints: spec.TopologySpreadConstraints,
					Containers:                []corev1.Container{container},
					Volumes: []corev1.Volume{{
						Name: configVolumeName,
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: n.queryConfigMap()},
							},
						},
					}},
				},
			},
		},
	}
}
