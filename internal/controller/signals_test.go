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
	"slices"
	"testing"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// signalCase describes what a single signal contributes to the config and the port lists.
type signalCase struct {
	name     string
	disable  func(*dbv1alpha1.SignalsSpec)
	cfgKeys  []string // config keys that must vanish when disabled
	portName []string // container/Service port names that must vanish when disabled
}

func signalCases() []signalCase {
	return []signalCase{
		{
			name:     "metrics",
			disable:  func(s *dbv1alpha1.SignalsSpec) { s.Metrics = ptr.To(false) },
			cfgKeys:  []string{"metrics_backend", "prometheus"},
			portName: []string{"prom-rw", "prom-http"},
		},
		{
			name:     "logs",
			disable:  func(s *dbv1alpha1.SignalsSpec) { s.Logs = ptr.To(false) },
			cfgKeys:  []string{"logs_backend", "loki"},
			portName: []string{"loki-http"},
		},
		{
			name:     "traces",
			disable:  func(s *dbv1alpha1.SignalsSpec) { s.Traces = ptr.To(false) },
			cfgKeys:  []string{"traces_backend", "tempo"},
			portName: []string{"tempo-http"},
		},
		{
			name:     "profiles",
			disable:  func(s *dbv1alpha1.SignalsSpec) { s.Profiles = ptr.To(false) },
			cfgKeys:  []string{"profiles_backend", "pyroscope"},
			portName: []string{"pyroscope"},
		},
	}
}

func renderedConfig(t *testing.T, cr *dbv1alpha1.OtelDBCluster) map[string]any {
	t.Helper()
	out, err := renderConfig(cr, cr.Spec.Etcd.Endpoints)
	if err != nil {
		t.Fatalf("renderConfig: %v", err)
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(out), &cfg); err != nil {
		t.Fatalf("unmarshal rendered config: %v\n%s", err, out)
	}
	return cfg
}

func containerPortNames(cr *dbv1alpha1.OtelDBCluster) []string {
	ports := buildStatefulSet(cr, "hash").Spec.Template.Spec.Containers[0].Ports
	names := make([]string, 0, len(ports))
	for _, p := range ports {
		names = append(names, p.Name)
	}
	return names
}

func servicePortNames(cr *dbv1alpha1.OtelDBCluster) []string {
	ports := buildClientService(cr).Spec.Ports
	names := make([]string, 0, len(ports))
	for _, p := range ports {
		names = append(names, p.Name)
	}
	return names
}

// A disabled signal must lose its backend key, its API bind and its ports, and nothing else.
func TestSignalDisabled(t *testing.T) {
	for _, tc := range signalCases() {
		t.Run(tc.name, func(t *testing.T) {
			cr := testCluster()
			tc.disable(&cr.Spec.Signals)

			cfg := renderedConfig(t, cr)
			for _, k := range tc.cfgKeys {
				if _, present := cfg[k]; present {
					t.Errorf("config key %q must be absent when %s is disabled", k, tc.name)
				}
			}
			// Other signals are untouched.
			for _, other := range signalCases() {
				if other.name == tc.name {
					continue
				}
				for _, k := range other.cfgKeys {
					if _, present := cfg[k]; !present {
						t.Errorf("config key %q must stay when only %s is disabled", k, tc.name)
					}
				}
			}
			if _, present := cfg["health_check"]; !present {
				t.Errorf("health_check must always be configured")
			}

			for _, want := range tc.portName {
				if names := containerPortNames(cr); slices.Contains(names, want) {
					t.Errorf("container port %q must be absent when %s is disabled: %v", want, tc.name, names)
				}
				if names := servicePortNames(cr); slices.Contains(names, want) {
					t.Errorf("service port %q must be absent when %s is disabled: %v", want, tc.name, names)
				}
			}
			// Ingest, self-metrics, health and peer ports survive regardless.
			for _, want := range []string{portNameOTLPGRPC, portNameOTLPHTTP, portNameSelfMetric, portNameHealth, portNamePeer} {
				if names := containerPortNames(cr); !slices.Contains(names, want) {
					t.Errorf("container port %q must stay when %s is disabled: %v", want, tc.name, names)
				}
			}
		})
	}
}

// Nil toggles keep every signal enabled, as documented by the kubebuilder defaults.
func TestSignalsNilKeepsEverything(t *testing.T) {
	cr := testCluster()
	cfg := renderedConfig(t, cr)
	for _, tc := range signalCases() {
		for _, k := range tc.cfgKeys {
			if _, present := cfg[k]; !present {
				t.Errorf("config key %q must be present with unset toggles", k)
			}
		}
		for _, want := range tc.portName {
			if names := containerPortNames(cr); !slices.Contains(names, want) {
				t.Errorf("container port %q must be present with unset toggles: %v", want, names)
			}
			if names := servicePortNames(cr); !slices.Contains(names, want) {
				t.Errorf("service port %q must be present with unset toggles: %v", want, names)
			}
		}
	}
}

func TestSignalsAllDisabledRejected(t *testing.T) {
	cr := testCluster()
	for _, tc := range signalCases() {
		tc.disable(&cr.Spec.Signals)
	}
	if err := validateSignals(cr); err == nil {
		t.Errorf("expected an error when every signal is disabled")
	}
	if _, err := renderConfig(cr, cr.Spec.Etcd.Endpoints); err == nil {
		t.Errorf("renderConfig must reject a cluster serving no signal")
	}
	if _, err := buildConfigMap(cr, cr.Spec.Etcd.Endpoints); err == nil {
		t.Errorf("buildConfigMap must propagate the validation error")
	}
}

func TestSignalsSingleEnabledAccepted(t *testing.T) {
	for _, tc := range signalCases() {
		t.Run(tc.name, func(t *testing.T) {
			cr := testCluster()
			// Disable everything but this one.
			for _, other := range signalCases() {
				if other.name != tc.name {
					other.disable(&cr.Spec.Signals)
				}
			}
			if err := validateSignals(cr); err != nil {
				t.Fatalf("only %s enabled must be valid: %v", tc.name, err)
			}
			cfg := renderedConfig(t, cr)
			for _, k := range tc.cfgKeys {
				if _, present := cfg[k]; !present {
					t.Errorf("config key %q must be present when %s is the only enabled signal", k, tc.name)
				}
			}
		})
	}
}
