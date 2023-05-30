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

package pgbackrest

import (
	"testing"

	"gotest.tools/v3/assert"

	ivory "github.com/highgo/ivory-operator/internal/ivory"
	"github.com/highgo/ivory-operator/pkg/apis/ivory-operator.highgo.com/v1beta1"
)

func TestIvorySQLParameters(t *testing.T) {
	cluster := new(v1beta1.IvoryCluster)
	parameters := new(ivory.Parameters)

	IvorySQL(cluster, parameters)
	assert.DeepEqual(t, parameters.Mandatory.AsMap(), map[string]string{
		"archive_mode":    "on",
		"archive_command": `pgbackrest --stanza=db archive-push "%p"`,
		"restore_command": `pgbackrest --stanza=db archive-get %f "%p"`,
	})

	assert.DeepEqual(t, parameters.Default.AsMap(), map[string]string{
		"archive_timeout": "60s",
	})

	cluster.Spec.Standby = &v1beta1.IvoryStandbySpec{
		Enabled:  true,
		RepoName: "repo99",
	}

	IvorySQL(cluster, parameters)
	assert.DeepEqual(t, parameters.Mandatory.AsMap(), map[string]string{
		"archive_mode":    "on",
		"archive_command": `pgbackrest --stanza=db archive-push "%p"`,
		"restore_command": `pgbackrest --stanza=db archive-get %f "%p" --repo=99`,
	})
}
