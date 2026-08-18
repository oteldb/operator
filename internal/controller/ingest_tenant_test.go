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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"

	dbv1alpha1 "github.com/oteldb/operator/api/v1alpha1"
)

// The default must stay today's behaviour: no tenant block, so odbingest installs no resolver and
// every write lands in "default" — the one tenant oteldb's read path can currently query.
func TestRenderIngestConfigTenantAbsentByDefault(t *testing.T) {
	cfg := renderedIngest(t, ingestCluster())
	require.NotContains(t, cfg, "tenant",
		"an ingest pool without spec.ingest.tenant must render exactly as before")
}

func TestRenderIngestConfigTenant(t *testing.T) {
	cr := ingestCluster()
	cr.Spec.Ingest.Tenant = &dbv1alpha1.TenantSpec{
		Header:             "X-Scope-OrgID",
		ResourceAttributes: []string{"service.namespace", "deployment.environment"},
		Default:            "shared",
		Require:            true,
	}

	tenant, ok := renderedIngest(t, cr)["tenant"].(map[string]any)
	require.True(t, ok, "tenant block missing")
	require.Equal(t, map[string]any{
		"header":              "X-Scope-OrgID",
		"resource_attributes": []any{"service.namespace", "deployment.environment"},
		"default":             "shared",
		"require":             true,
	}, tenant)
}

func TestRenderIngestConfigTenantOmitsUnsetKeys(t *testing.T) {
	cr := ingestCluster()
	cr.Spec.Ingest.Tenant = &dbv1alpha1.TenantSpec{Default: "team-a"}

	tenant := renderedIngest(t, cr)["tenant"].(map[string]any)
	require.Equal(t, map[string]any{"default": "team-a"}, tenant,
		"unset sources must be omitted rather than rendered empty")
}

// The tenant block is modelled in full, so extraConfig may not reach around the spec into it.
func TestRenderIngestConfigReservedTenant(t *testing.T) {
	cr := ingestCluster()
	cr.Spec.Ingest.ExtraConfig = &runtime.RawExtension{
		Raw: []byte(`{"tenant":{"header":"X-Scope-OrgID"}}`),
	}

	_, err := renderIngestConfig(cr, cr.Spec.Etcd.Endpoints)
	require.ErrorContains(t, err, "tenant (use spec.ingest.tenant)")

	var invalid validationError
	require.True(t, errors.As(err, &invalid), "reserved paths must fail as a spec validation error")
}

func TestValidateIngestTenant(t *testing.T) {
	tests := []struct {
		name    string
		tenant  *dbv1alpha1.TenantSpec
		wantErr string
	}{
		{
			name: "absent is valid",
		},
		{
			name:   "a header alone",
			tenant: &dbv1alpha1.TenantSpec{Header: "X-Scope-OrgID"},
		},
		{
			name:   "a required header",
			tenant: &dbv1alpha1.TenantSpec{Header: "X-Scope-OrgID", Require: true},
		},
		{
			name:    "require without a header",
			tenant:  &dbv1alpha1.TenantSpec{Default: "a", Require: true},
			wantErr: "spec.ingest.tenant.require needs spec.ingest.tenant.header",
		},
		{
			name:    "no source at all",
			tenant:  &dbv1alpha1.TenantSpec{},
			wantErr: "spec.ingest.tenant sets no source",
		},
		{
			name:    "an empty attribute key",
			tenant:  &dbv1alpha1.TenantSpec{ResourceAttributes: []string{"service.namespace", ""}},
			wantErr: "spec.ingest.tenant.resourceAttributes[1] must not be empty",
		},
		{
			name:    "a default with a path separator",
			tenant:  &dbv1alpha1.TenantSpec{Default: "team/a"},
			wantErr: `invalid character "/" in tenant id`,
		},
		{
			name:    "a default that is a relative path",
			tenant:  &dbv1alpha1.TenantSpec{Default: ".."},
			wantErr: `invalid tenant id ".."`,
		},
		{
			name:    "a default past the length bound",
			tenant:  &dbv1alpha1.TenantSpec{Default: strings.Repeat("a", 151)},
			wantErr: "tenant id longer than 150 bytes",
		},
		{
			name:   "a default at the length bound",
			tenant: &dbv1alpha1.TenantSpec{Default: strings.Repeat("a", 150)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cr := ingestCluster()
			cr.Spec.Ingest.Tenant = tt.tenant

			err := validateIngestTenant(cr)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)

			var invalid validationError
			require.True(t, errors.As(err, &invalid), "must be reported as a spec validation error")
		})
	}
}

// A tenant block on a cluster with no ingest pool is inert rather than an error: spec.ingest is
// what carries it, so it cannot outlive its pool.
func TestValidateIngestTenantWithoutPool(t *testing.T) {
	require.NoError(t, validateIngestTenant(testCluster()))
}
