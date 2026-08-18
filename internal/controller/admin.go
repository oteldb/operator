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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

func adminEnabled(cr *dbv1alpha1.OtelDBCluster) bool { return cr.Spec.Admin != nil }

// buildAdminService publishes oteldb's admin API, which every storage pod already serves on
// portAdmin, on a Service of its own.
//
// It targets the port by number rather than by name on purpose. oteldb registers the admin server
// unconditionally (cmd/oteldb/admin.go: "It is always registered", and its bind block defaults to
// :8090 whether or not the config declares it), so the listener exists regardless of this Service.
// Declaring a matching container port would therefore document nothing the pod does not already do,
// while changing the pod template — which would roll the whole StatefulSet on a change that only
// adds a Service. Toggling spec.admin stays a Service-only operation.
func buildAdminService(cr *dbv1alpha1.OtelDBCluster) *corev1.Service {
	n := namesFor(cr)
	svcType := cr.Spec.Admin.Service.Type
	if svcType == "" {
		svcType = corev1.ServiceTypeClusterIP
	}

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      n.adminService(),
			Namespace: cr.Namespace,
			// The component label separates this from the client Service, so a ServiceMonitor or a
			// NetworkPolicy can select the admin endpoint without matching client traffic.
			Labels:      roleCommonLabels(cr, appName, valAdmin),
			Annotations: cr.Spec.Admin.Service.Annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     svcType,
			Selector: selectorLabels(cr),
			Ports: []corev1.ServicePort{{
				Name:       portNameAdmin,
				Port:       portAdmin,
				TargetPort: intstr.FromInt32(portAdmin),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}
