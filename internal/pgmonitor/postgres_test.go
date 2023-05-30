/*
 Copyright 2021 - 2023 Highgo Solutions, Inc.
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

package pgmonitor

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	ivory "github.com/highgo/ivory-operator/internal/ivory"
	"github.com/highgo/ivory-operator/pkg/apis/ivory-operator.highgo.com/v1beta1"
)

func TestIvorySQLHBA(t *testing.T) {
	t.Run("ExporterDisabled", func(t *testing.T) {
		inCluster := &v1beta1.IvoryCluster{}
		outHBAs := ivory.HBAs{}
		IvorySQLHBAs(inCluster, &outHBAs)
		assert.Equal(t, len(outHBAs.Mandatory), 0)
	})

	t.Run("ExporterEnabled", func(t *testing.T) {
		inCluster := &v1beta1.IvoryCluster{}
		inCluster.Spec.Monitoring = &v1beta1.MonitoringSpec{
			PGMonitor: &v1beta1.PGMonitorSpec{
				Exporter: &v1beta1.ExporterSpec{
					Image: "image",
				},
			},
		}

		outHBAs := ivory.HBAs{}
		IvorySQLHBAs(inCluster, &outHBAs)

		assert.Equal(t, len(outHBAs.Mandatory), 3)
		assert.Equal(t, outHBAs.Mandatory[0].String(), `host all "ccp_monitoring" "127.0.0.0/8" scram-sha-256`)
		assert.Equal(t, outHBAs.Mandatory[1].String(), `host all "ccp_monitoring" "::1/128" scram-sha-256`)
		assert.Equal(t, outHBAs.Mandatory[2].String(), `host all "ccp_monitoring" all reject`)
	})
}

func TestIvorySQLParameters(t *testing.T) {
	t.Run("ExporterDisabled", func(t *testing.T) {
		inCluster := &v1beta1.IvoryCluster{}
		outParameters := ivory.NewParameters()
		IvorySQLParameters(inCluster, &outParameters)
		assert.Assert(t, !outParameters.Mandatory.Has("shared_preload_libraries"))
	})

	t.Run("ExporterEnabled", func(t *testing.T) {
		inCluster := &v1beta1.IvoryCluster{}
		inCluster.Spec.Monitoring = &v1beta1.MonitoringSpec{
			PGMonitor: &v1beta1.PGMonitorSpec{
				Exporter: &v1beta1.ExporterSpec{
					Image: "image",
				},
			},
		}
		outParameters := ivory.NewParameters()

		IvorySQLParameters(inCluster, &outParameters)
		libs, found := outParameters.Mandatory.Get("shared_preload_libraries")
		assert.Assert(t, found)
		assert.Assert(t, strings.Contains(libs, "pg_stat_statements"))
		assert.Assert(t, strings.Contains(libs, "pgnodemx"))
	})

	t.Run("SharedPreloadLibraries Defined", func(t *testing.T) {
		inCluster := &v1beta1.IvoryCluster{}
		inCluster.Spec.Monitoring = &v1beta1.MonitoringSpec{
			PGMonitor: &v1beta1.PGMonitorSpec{
				Exporter: &v1beta1.ExporterSpec{
					Image: "image",
				},
			},
		}
		outParameters := ivory.NewParameters()
		outParameters.Mandatory.Add("shared_preload_libraries", "daisy")

		IvorySQLParameters(inCluster, &outParameters)
		libs, found := outParameters.Mandatory.Get("shared_preload_libraries")
		assert.Assert(t, found)
		assert.Assert(t, strings.Contains(libs, "pg_stat_statements"))
		assert.Assert(t, strings.Contains(libs, "pgnodemx"))
		assert.Assert(t, strings.Contains(libs, "daisy"))
	})
}
