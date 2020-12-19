#!/usr/bin/env bash

# Copyright 2020 Crunchy Data Solutions, Inc.
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -eu

# Create and cleanup a temporary directory.
DIR="$(mktemp -d)"
trap "rm -rf '$DIR'" EXIT

# Find the Go install path.
[ "${GOBIN:-}" ] || GOBIN="$(go env GOBIN)"
[ "${GOBIN:-}" ] || GOBIN="$(go env GOPATH)/bin"

# Find `controller-gen` on the current PATH or install it to the Go install path.
tool="$(command -v controller-gen || true)"
[ -n "$tool" ] || tool="$GOBIN/controller-gen"
[ -x "$tool" ] || ( cd "$DIR" && go mod init tmp && go get 'sigs.k8s.io/controller-tools/cmd/controller-gen@v0.4.1' )

"$tool" "$@"