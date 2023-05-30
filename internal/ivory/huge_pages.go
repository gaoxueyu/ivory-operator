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

package ivory

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/highgo/ivory-operator/pkg/apis/ivory-operator.highgo.com/v1beta1"
)

// This function looks for a valid huge_pages resource request. If it finds one,
// it sets the IvorySQL parameter "huge_pages" to "try". If it doesn't find
// one, it sets "huge_pages" to "off".
func SetHugePages(cluster *v1beta1.IvoryCluster, pgParameters *Parameters) {
	if hugePagesRequested(cluster) {
		pgParameters.Default.Add("huge_pages", "try")
	} else {
		pgParameters.Default.Add("huge_pages", "off")
	}
}

// This helper function checks to see if a huge_pages value greater than zero has
// been set in any of the IvoryCluster's instances' resource specs
func hugePagesRequested(cluster *v1beta1.IvoryCluster) bool {
	for _, instance := range cluster.Spec.InstanceSets {
		for resourceName := range instance.Resources.Limits {
			if strings.HasPrefix(resourceName.String(), corev1.ResourceHugePagesPrefix) {
				resourceQuantity := instance.Resources.Limits.Name(resourceName, resource.BinarySI)

				if resourceQuantity != nil && resourceQuantity.Value() > 0 {
					return true
				}
			}
		}
	}

	return false
}
