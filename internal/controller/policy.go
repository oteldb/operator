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

// maxECShards is the total shard ceiling the storage library enforces on an erasure-coding scheme
// (github.com/oteldb/storage/cluster/ec, Scheme.Validate). Past it the scheme is rejected
// downstream and the tenant silently falls back to full-copy.
const maxECShards = 256

// oteldb storage.policy config keys.
const (
	keyPolicy     = "policy"
	keyRetention  = "retention"
	keyLimits     = "limits"
	keyDownsample = "downsample"
	keyPrecision  = "precision"
	keyRecompress = "recompress"
	keyEC         = "ec"
	keyAfter      = "after"
	keyInterval   = "interval"
	keyECData     = "data"
	keyECParity   = "parity"
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
	if e := spec.EC; e != nil {
		m := map[string]any{
			keyECData:   e.Data,
			keyECParity: e.Parity,
		}
		// An absent after erasure-codes every part; oteldb spells that as the zero duration, which
		// is what an omitted key decodes to.
		if e.After != nil {
			m[keyAfter] = e.After.Duration.String()
		}
		policy[keyEC] = m
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

	if err := validateMergeTiers(cr.Spec.Policy); err != nil {
		return err
	}
	return validateEC(cr)
}

// validateEC rejects an erasure-coding policy the engine would ignore or could not place. EC is
// the one policy whose preconditions are topology, not values, and every one of them fails
// silently: oteldb warns and carries on, the ring clamps an oversized scheme to the members it
// has, and a configured replication factor is dropped without a word. The operator owns the
// topology, so it can turn each of those into a spec error instead.
func validateEC(cr *dbv1alpha1.OtelDBCluster) error {
	e := cr.Spec.Policy.EC
	if e == nil {
		return nil
	}

	// Data and Parity are already bounded below by the CRD; only the joint bound is left, and it
	// is the storage library's ([ec.Scheme].Validate).
	if shards := int64(e.Data) + int64(e.Parity); shards > maxECShards {
		return invalidSpec("spec.policy.ec has %d shards (data %d + parity %d), at most %d are allowed",
			shards, e.Data, e.Parity, maxECShards)
	}
	if e.After != nil && e.After.Duration < 0 {
		return invalidSpec("spec.policy.ec.after must not be negative, got %s", e.After.Duration)
	}

	// Erasure coding needs a shared-nothing cluster. Cluster mode is a given here (spec.etcd is
	// required), so a private backend is the only half that can be missing.
	if !privateBackendOf(cr) {
		return invalidSpec("spec.policy.ec needs a private per-node backend, but spec.storage.backend " +
			"is s3 (or spec.cluster.privateBackend is false): a shared object store owns durability " +
			"itself, so every part would stay full-copy and the policy would do nothing")
	}

	// Data+Parity is the owner count, so the shards need that many nodes to land on. The ring
	// clamps to the members it has instead of failing, which converts parts into a scheme the
	// cluster cannot actually spread.
	if shards := e.Data + e.Parity; shards > replicasOf(cr) {
		return invalidSpec("spec.policy.ec spreads %d shards (data %d + parity %d) one per node, "+
			"but spec.replicas is %d", shards, e.Data, e.Parity, replicasOf(cr))
	}

	// Under EC the owner count is Data+Parity and the tenant's replication factor is ignored —
	// for the flushed parts and for the unflushed head alike. Honouring one and dropping the other
	// is exactly the surprise worth refusing: the CR would document a replication factor the
	// cluster does not use.
	if rf := cr.Spec.Cluster.ReplicationFactor; rf != nil {
		return invalidSpec("spec.policy.ec and spec.cluster.replicationFactor are mutually exclusive: "+
			"erasure coding fixes the owner count at data+parity (%d), and the replication factor "+
			"(%d) is ignored; drop spec.cluster.replicationFactor", e.Data+e.Parity, *rf)
	}

	// A tier that only applies after the data is gone is merge work whose output is dropped
	// unread, the same rule the other cold tiers get.
	if maxAge := cr.Spec.Policy.Retention.MaxAge; maxAge != nil && maxAge.Duration > 0 &&
		e.After != nil && e.After.Duration >= maxAge.Duration {
		return invalidSpec("spec.policy.ec.after (%s) is at or past spec.policy.retention.maxAge (%s): "+
			"parts are dropped before they are erasure-coded", e.After.Duration, maxAge.Duration)
	}

	return nil
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
