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
	"fmt"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// signalEnabled reads a signal toggle; an unset toggle means enabled.
func signalEnabled(toggle *bool) bool {
	if toggle != nil {
		return *toggle
	}
	return true
}

func metricsEnabled(cr *dbv1alpha1.OtelDBCluster) bool {
	return signalEnabled(cr.Spec.Signals.Metrics)
}

func logsEnabled(cr *dbv1alpha1.OtelDBCluster) bool {
	return signalEnabled(cr.Spec.Signals.Logs)
}

func tracesEnabled(cr *dbv1alpha1.OtelDBCluster) bool {
	return signalEnabled(cr.Spec.Signals.Traces)
}

func profilesEnabled(cr *dbv1alpha1.OtelDBCluster) bool {
	return signalEnabled(cr.Spec.Signals.Profiles)
}

// validateSignals rejects a cluster that would serve no signal at all.
func validateSignals(cr *dbv1alpha1.OtelDBCluster) error {
	if metricsEnabled(cr) || logsEnabled(cr) || tracesEnabled(cr) || profilesEnabled(cr) {
		return nil
	}
	return fmt.Errorf("spec.signals: at least one signal must be enabled")
}
