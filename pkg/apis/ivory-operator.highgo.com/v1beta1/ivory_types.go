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

package v1beta1

// IvorySQL identifiers are limited in length but may contain any character.
// More info: https://www.ivorysql.org/docs/current/sql-syntax-lexical.html#SQL-SYNTAX-IDENTIFIERS
//
// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=63
type IvoryIdentifier string

type IvoryPasswordSpec struct {
	// Type of password to generate. Defaults to ASCII. Valid options are ASCII
	// and AlphaNumeric.
	// "ASCII" passwords contain letters, numbers, and symbols from the US-ASCII character set.
	// "AlphaNumeric" passwords contain letters and numbers from the US-ASCII character set.
	// +kubebuilder:default=ASCII
	// +kubebuilder:validation:Enum={ASCII,AlphaNumeric}
	Type string `json:"type"`
}

// IvoryPasswordSpec types.
const (
	IvoryPasswordTypeAlphaNumeric = "AlphaNumeric"
	IvoryPasswordTypeASCII        = "ASCII"
)

type IvoryUserSpec struct {

	// This value goes into the name of a corev1.Secret and a label value, so
	// it must match both IsDNS1123Subdomain and IsValidLabelValue. The pattern
	// below is IsDNS1123Subdomain without any dots, U+002E.

	// The name of this IvorySQL user. The value may contain only lowercase
	// letters, numbers, and hyphen so that it fits into Kubernetes metadata.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:Type=string
	Name IvoryIdentifier `json:"name"`

	// Databases to which this user can connect and create objects. Removing a
	// database from this list does NOT revoke access. This field is ignored for
	// the "ivory" user.
	// +listType=set
	// +optional
	Databases []IvoryIdentifier `json:"databases,omitempty"`

	// ALTER ROLE options except for PASSWORD. This field is ignored for the
	// "ivory" user.
	// More info: https://www.ivorysql.org/docs/current/role-attributes.html
	// +kubebuilder:validation:Pattern=`^[^;]*$`
	// +optional
	Options string `json:"options,omitempty"`

	// Properties of the password generated for this user.
	// +optional
	Password *IvoryPasswordSpec `json:"password,omitempty"`
}
