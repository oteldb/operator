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

	"k8s.io/apimachinery/pkg/api/resource"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// oteldb storage.policy config keys.
const (
	keyPolicy     = "policy"
	keyRetention  = "retention"
	keyLimits     = "limits"
	keyDownsample = "downsample"
	keyPrecision  = "precision"
	keyRecompress = "recompress"
	keyAfter      = "after"
	keyInterval   = "interval"
)

// renderPolicy builds the storage.policy block from spec.policy, or returns nil when nothing is
// configured — oteldb installs no tenancy resolver for an absent policy, which is the library
// default (retain forever, no limits, lossless, no rollup).
func renderPolicy(cr *dbv1alpha1.OtelDBCluster) map[string]any {
	spec := cr.Spec.Policy
	policy := map[string]any{}

	if retention := renderRetention(spec.Retention); len(retention) > 0 {
		policy[keyRetention] = retention
	}
	if limits := renderLimits(spec.Limits); len(limits) > 0 {
		policy[keyLimits] = limits
	}
	if tiers := renderDownsample(spec.Downsample); len(tiers) > 0 {
		policy[keyDownsample] = tiers
	}
	if tiers := renderPrecision(spec.Precision); len(tiers) > 0 {
		policy[keyPrecision] = tiers
	}
	if r := spec.Recompress; r != nil {
		m := map[string]any{keyAfter: r.After.Duration.String()}
		if r.Level != nil {
			m["level"] = *r.Level
		}
		policy[keyRecompress] = m
	}

	if len(policy) == 0 {
		return nil
	}
	return policy
}

func renderDownsample(tiers []dbv1alpha1.DownsampleTierSpec) []any {
	out := make([]any, 0, len(tiers))
	for _, t := range tiers {
		m := map[string]any{
			keyAfter:    t.After.Duration.String(),
			keyInterval: t.Interval.Duration.String(),
		}
		if t.Agg != "" {
			m["agg"] = t.Agg
		}
		out = append(out, m)
	}
	return out
}

func renderPrecision(tiers []dbv1alpha1.PrecisionTierSpec) []any {
	out := make([]any, 0, len(tiers))
	for _, t := range tiers {
		out = append(out, map[string]any{
			keyAfter: t.After.Duration.String(),
			"bits":   t.Bits,
		})
	}
	return out
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

	return validateMergeTiers(cr.Spec.Policy)
}

// validateMergeTiers rejects downsample/precision/recompress settings the engine would ignore. The
// tiers are lossy and irreversible, so a tier that silently does nothing is worth failing over: the
// user believes their old data is being coarsened when it is not.
func validateMergeTiers(spec dbv1alpha1.PolicySpec) error {
	seen := map[string]int{}
	for i, t := range spec.Downsample {
		field := fmt.Sprintf("spec.policy.downsample[%d]", i)
		if t.After.Duration < 0 {
			return invalidSpec("%s.after must not be negative, got %s", field, t.After.Duration)
		}
		if t.Interval.Duration <= 0 {
			return invalidSpec("%s.interval must be positive, got %s", field, t.Interval.Duration)
		}
		if prev, dup := seen[t.After.Duration.String()]; dup {
			return invalidSpec("%s.after duplicates spec.policy.downsample[%d].after (%s): "+
				"a sample takes one tier, so the other is dead", field, prev, t.After.Duration)
		}
		seen[t.After.Duration.String()] = i
	}

	seen = map[string]int{}
	for i, t := range spec.Precision {
		field := fmt.Sprintf("spec.policy.precision[%d]", i)
		if t.After.Duration < 0 {
			return invalidSpec("%s.after must not be negative, got %s", field, t.After.Duration)
		}
		if prev, dup := seen[t.After.Duration.String()]; dup {
			return invalidSpec("%s.after duplicates spec.policy.precision[%d].after (%s): "+
				"a part takes one tier, so the other is dead", field, prev, t.After.Duration)
		}
		seen[t.After.Duration.String()] = i
	}

	if r := spec.Recompress; r != nil && r.After.Duration <= 0 {
		return invalidSpec("spec.policy.recompress.after must be positive, got %s; "+
			"remove the recompress block to disable it", r.After.Duration)
	}

	// Coarsening past the retention window is merge work whose output is dropped before it can be
	// read. Zero maxAge is "retain forever", so only a real window is checked.
	if maxAge := spec.Retention.MaxAge; maxAge != nil && maxAge.Duration > 0 {
		for i, t := range spec.Downsample {
			if t.After.Duration >= maxAge.Duration {
				return invalidSpec("spec.policy.downsample[%d].after (%s) is at or past "+
					"spec.policy.retention.maxAge (%s): the tier never applies before the data is dropped",
					i, t.After.Duration, maxAge.Duration)
			}
		}
		for i, t := range spec.Precision {
			if t.After.Duration >= maxAge.Duration {
				return invalidSpec("spec.policy.precision[%d].after (%s) is at or past "+
					"spec.policy.retention.maxAge (%s): the tier never applies before the data is dropped",
					i, t.After.Duration, maxAge.Duration)
			}
		}
		if r := spec.Recompress; r != nil && r.After.Duration >= maxAge.Duration {
			return invalidSpec("spec.policy.recompress.after (%s) is at or past "+
				"spec.policy.retention.maxAge (%s): parts are dropped before they are recompressed",
				r.After.Duration, maxAge.Duration)
		}
	}
	return nil
}
