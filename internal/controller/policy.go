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
	"k8s.io/apimachinery/pkg/api/resource"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// oteldb storage.policy config keys.
const (
	keyPolicy    = "policy"
	keyRetention = "retention"
	keyLimits    = "limits"
)

// renderPolicy builds the storage.policy block from spec.retention and spec.limits, or returns nil
// when neither is set — oteldb installs no tenancy resolver for an absent policy, which is the
// library default (retain forever, no limits).
func renderPolicy(cr *dbv1alpha1.OtelDBCluster) map[string]any {
	policy := map[string]any{}

	if retention := renderRetention(cr.Spec.Policy.Retention); len(retention) > 0 {
		policy[keyRetention] = retention
	}
	if limits := renderLimits(cr.Spec.Policy.Limits); len(limits) > 0 {
		policy[keyLimits] = limits
	}

	if len(policy) == 0 {
		return nil
	}
	return policy
}

func renderRetention(spec dbv1alpha1.RetentionSpec) map[string]any {
	m := map[string]any{}
	if spec.MaxAge != nil {
		m["max_age"] = spec.MaxAge.Duration.String()
	}
	if spec.MaxBytes != nil {
		m["max_bytes"] = spec.MaxBytes.Value()
	}
	return m
}

func renderLimits(spec dbv1alpha1.LimitsSpec) map[string]any {
	m := map[string]any{}
	if spec.IngestBytesPerSecond != nil {
		m["ingest_bytes_per_second"] = spec.IngestBytesPerSecond.Value()
	}
	if spec.MaxInFlightBytes != nil {
		m["max_in_flight_bytes"] = spec.MaxInFlightBytes.Value()
	}
	if spec.MaxSeries != nil {
		m["max_series"] = *spec.MaxSeries
	}
	if spec.MaxSeriesSoft != nil {
		m["max_series_soft"] = *spec.MaxSeriesSoft
	}
	if spec.MaxPartSize != nil {
		m["max_part_size"] = spec.MaxPartSize.Value()
	}
	return m
}

// validatePolicy rejects retention and limit values oteldb would silently treat as "unset" or that
// contradict each other. A negative quantity is always a mistake; the zero value is the documented
// "unlimited", so it is left alone.
func validatePolicy(cr *dbv1alpha1.OtelDBCluster) error {
	retention := cr.Spec.Policy.Retention
	if retention.MaxAge != nil && retention.MaxAge.Duration < 0 {
		return invalidSpec("spec.policy.retention.maxAge must not be negative, got %s", retention.MaxAge.Duration)
	}
	limits := cr.Spec.Policy.Limits
	for _, q := range []struct {
		field string
		value *resource.Quantity
	}{
		{"spec.policy.retention.maxBytes", retention.MaxBytes},
		{"spec.policy.limits.ingestBytesPerSecond", limits.IngestBytesPerSecond},
		{"spec.policy.limits.maxInFlightBytes", limits.MaxInFlightBytes},
		{"spec.policy.limits.maxPartSize", limits.MaxPartSize},
	} {
		if q.value != nil && q.value.Sign() < 0 {
			return invalidSpec("%s must not be negative, got %s", q.field, q.value.String())
		}
	}

	// A soft budget above the hard ceiling never engages: the hard limit sheds first, so the
	// overflow series the soft budget promises are never minted.
	if limits.MaxSeriesSoft != nil && *limits.MaxSeriesSoft > 0 {
		if limits.MaxSeries == nil || *limits.MaxSeries <= 0 {
			return invalidSpec("spec.policy.limits.maxSeriesSoft needs spec.policy.limits.maxSeries to be set")
		}
		if *limits.MaxSeriesSoft > *limits.MaxSeries {
			return invalidSpec("spec.policy.limits.maxSeriesSoft (%d) must not exceed spec.policy.limits.maxSeries (%d)",
				*limits.MaxSeriesSoft, *limits.MaxSeries)
		}
	}
	return nil
}
