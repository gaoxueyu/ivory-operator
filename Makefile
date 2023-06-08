PGO_IMAGE_NAME ?= postgres-operator
PGO_IMAGE_MAINTAINER ?= Crunchy Data
PGO_IMAGE_SUMMARY ?= Crunchy PostgreSQL Operator
PGO_IMAGE_DESCRIPTION ?= $(PGO_IMAGE_SUMMARY)
PGO_IMAGE_URL ?= https://www.crunchydata.com/products/crunchy-postgresql-for-kubernetes
PGO_IMAGE_PREFIX ?= localhost

CRUNCHY_POSTGRES_EXPORTER_IMAGE_NAME ?= crunchy-postgres-exporter
CRUNCHY_POSTGRES_EXPORTER_MAINTAINER ?= $(PGO_IMAGE_MAINTAINER)
CRUNCHY_POSTGRES_EXPORTER_SUMMARY ?= Metrics exporter for PostgreSQL
CRUNCHY_POSTGRES_EXPORTER_DESCRIPTION ?= \
	When run with the crunchy-postgres family of containers, crunchy-postgres-exporter reads the PostgreSQL data directory \
	and has a SQL interface to a database to allow for metrics collection.
CRUNCHY_POSTGRES_EXPORTER_URL ?= https://www.crunchydata.com/products/crunchy-postgresql-for-kubernetes
CRUNCHY_POSTGRES_EXPORTER_IMAGE_PREFIX ?= $(PGO_IMAGE_PREFIX)
CRUNCHY_POSTGRES_EXPORTER_PG_VERSION ?= 15
CRUNCHY_POSTGRES_EXPORTER_PG_FULL_VERSION ?= 15.2

PGMONITOR_DIR ?= hack/tools/pgmonitor
PGMONITOR_VERSION ?= 'v4.8.0'
POSTGRES_EXPORTER_VERSION ?= 0.10.1
POSTGRES_EXPORTER_URL ?= https://github.com/prometheus-community/postgres_exporter/releases/download/v${POSTGRES_EXPORTER_VERSION}/postgres_exporter-${POSTGRES_EXPORTER_VERSION}.linux-amd64.tar.gz

# Buildah's "build" used to be "bud". Use the alias to be compatible for a while.
BUILDAH_BUILD ?= buildah bud

DEBUG_BUILD ?= false
GO ?= go
GO_BUILD = $(GO_CMD) build -trimpath
GO_CMD = $(GO_ENV) $(GO)
GO_TEST ?= $(GO) test
KUTTL ?= kubectl-kuttl
KUTTL_TEST ?= $(KUTTL) test

# Disable optimizations if creating a debug build
ifeq ("$(DEBUG_BUILD)", "true")
	GO_BUILD = $(GO_CMD) build -gcflags='all=-N -l'
endif

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-formatting the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: all
all: ## Build all images
all: build-postgres-operator-image
all: build-crunchy-postgres-exporter-image

.PHONY: setup
setup: ## Run Setup needed to build images
setup: get-pgmonitor
setup: get-postgres-exporter

.PHONY: get-pgmonitor
get-pgmonitor:
	git -C '$(dir $(PGMONITOR_DIR))' clone https://github.com/CrunchyData/pgmonitor.git || git -C '$(PGMONITOR_DIR)' fetch origin
	@git -C '$(PGMONITOR_DIR)' checkout '$(PGMONITOR_VERSION)'
	@git -C '$(PGMONITOR_DIR)' config pull.ff only

.PHONY: get-postgres-exporter
get-postgres-exporter:
	[ ! -e hack/tools/postgres_exporter.tar.gz ] || (rm hack/tools/postgres_exporter.tar.gz && echo "Deleting old exporter")
	wget -O hack/tools/postgres_exporter.tar.gz '$(POSTGRES_EXPORTER_URL)'

.PHONY: clean
clean: ## Clean resources
clean: clean-deprecated
	rm -f bin/postgres-operator
	rm -f config/rbac/role.yaml
	[ ! -d testing/kuttl/e2e-generated ] || rm -r testing/kuttl/e2e-generated
	[ ! -d testing/kuttl/e2e-generated-other ] || rm -r testing/kuttl/e2e-generated-other
	rm -rf build/crd/generated build/crd/*/generated
	[ ! -f hack/tools/setup-envtest ] || hack/tools/setup-envtest --bin-dir=hack/tools/envtest cleanup
	[ ! -f hack/tools/setup-envtest ] || rm hack/tools/setup-envtest
	[ ! -d hack/tools/envtest ] || rm -r hack/tools/envtest
	[ ! -d hack/tools/pgmonitor ] || rm -rf hack/tools/pgmonitor
	[ ! -n "$$(ls hack/tools)" ] || rm -r hack/tools/*
	[ ! -d hack/.kube ] || rm -r hack/.kube

.PHONY: clean-deprecated
clean-deprecated: ## Clean deprecated resources
	@# packages used to be downloaded into the vendor directory
	[ ! -d vendor ] || rm -r vendor
	@# executables used to be compiled into the $GOBIN directory
	[ ! -n '$(GOBIN)' ] || rm -f $(GOBIN)/postgres-operator $(GOBIN)/apiserver $(GOBIN)/*pgo
	@# executables used to be in subdirectories
	[ ! -d bin/pgo-rmdata ] || rm -r bin/pgo-rmdata
	[ ! -d bin/pgo-backrest ] || rm -r bin/pgo-backrest
	[ ! -d bin/pgo-scheduler ] || rm -r bin/pgo-scheduler
	[ ! -d bin/postgres-operator ] || rm -r bin/postgres-operator
	@# keys used to be generated before install
	[ ! -d conf/pgo-backrest-repo ] || rm -r conf/pgo-backrest-repo
	[ ! -d conf/postgres-operator ] || rm -r conf/postgres-operator

##@ Deployment
.PHONY: createnamespaces
createnamespaces: ## Create operator and target namespaces
	kubectl apply -k ./config/namespace

.PHONY: deletenamespaces
deletenamespaces: ## Delete operator and target namespaces
	kubectl delete -k ./config/namespace

.PHONY: install
install: ## Install the postgrescluster CRD
	kubectl apply --server-side -k ./config/crd

.PHONY: uninstall
uninstall: ## Delete the postgrescluster CRD
	kubectl delete -k ./config/crd

.PHONY: deploy
deploy: ## Deploy the PostgreSQL Operator (enables the postgrescluster controller)
	kubectl apply --server-side -k ./config/default

.PHONY: undeploy
undeploy: ## Undeploy the PostgreSQL Operator
	kubectl delete -k ./config/default

.PHONY: deploy-dev
deploy-dev: ## Deploy the PostgreSQL Operator locally
deploy-dev: PGO_FEATURE_GATES ?= "TablespaceVolumes=true"
deploy-dev: build-postgres-operator
deploy-dev: createnamespaces
	kubectl apply --server-side -k ./config/dev
	hack/create-kubeconfig.sh ivory-operator pgo
	env \
		CRUNCHY_DEBUG=true \
		PGO_FEATURE_GATES="${PGO_FEATURE_GATES}" \
		CHECK_FOR_UPGRADES='$(if $(CHECK_FOR_UPGRADES),$(CHECK_FOR_UPGRADES),false)' \
		KUBECONFIG=hack/.kube/ivory-operator/pgo \
		IVYO_NAMESPACE='ivory-operator' \
		$(shell kubectl kustomize ./config/dev | \
			sed -ne '/^kind: Deployment/,/^---/ { \
				/RELATED_IMAGE_/ { N; s,.*\(RELATED_[^[:space:]]*\).*value:[[:space:]]*\([^[:space:]]*\),\1="\2",; p; }; \
			}') \
		$(foreach v,$(filter RELATED_IMAGE_%,$(.VARIABLES)),$(v)="$($(v))") \
		bin/postgres-operator

##@ Build - Binary
.PHONY: build-postgres-operator
build-postgres-operator: ## Build the postgres-operator binary
	$(GO_BUILD) -ldflags '-X "main.versionString=$(PGO_VERSION)"' \
		-o bin/postgres-operator ./cmd/ivory-operator

##@ Build - Images
.PHONY: build-crunchy-postgres-exporter-image
build-crunchy-postgres-exporter-image: ## Build the crunchy-postgres-exporter image
build-crunchy-postgres-exporter-image: CRUNCHY_POSTGRES_EXPORTER_IMAGE_REVISION := $(shell git rev-parse HEAD)
build-crunchy-postgres-exporter-image: CRUNCHY_POSTGRES_EXPORTER_IMAGE_TIMESTAMP := $(shell date -u +%FT%TZ)
build-crunchy-postgres-exporter-image: build/crunchy-postgres-exporter/Dockerfile
	$(if $(shell (echo 'buildah version 1.24'; $(word 1,$(BUILDAH_BUILD)) --version) | sort -Vc 2>&1), \
		$(warning WARNING: old buildah does not invalidate its cache for changed labels: \
			https://github.com/containers/buildah/issues/3517))
	$(if $(IMAGE_TAG),,	$(error missing IMAGE_TAG))
	$(BUILDAH_BUILD) \
		--tag $(BUILDAH_TRANSPORT)$(CRUNCHY_POSTGRES_EXPORTER_IMAGE_PREFIX)/$(CRUNCHY_POSTGRES_EXPORTER_IMAGE_NAME):$(IMAGE_TAG) \
		--build-arg PGVERSION=$(CRUNCHY_POSTGRES_EXPORTER_PG_VERSION) \
		--label name='$(CRUNCHY_POSTGRES_EXPORTER_IMAGE_NAME)' \
		--label build-date='$(CRUNCHY_POSTGRES_EXPORTER_IMAGE_TIMESTAMP)' \
		--label description='$(CRUNCHY_POSTGRES_EXPORTER_DESCRIPTION)' \
		--label maintainer='$(CRUNCHY_POSTGRES_EXPORTER_MAINTAINER)' \
		--label summary='$(CRUNCHY_POSTGRES_EXPORTER_SUMMARY)' \
		--label url='$(CRUNCHY_POSTGRES_EXPORTER_URL)' \
		--label vcs-ref='$(CRUNCHY_POSTGRES_EXPORTER_IMAGE_REVISION)' \
		--label vendor='$(CRUNCHY_POSTGRES_EXPORTER_MAINTAINER)' \
		--label postgres.version.major='$(CRUNCHY_POSTGRES_EXPORTER_PG_VERSION)' \
		--label postgres.version='$(CRUNCHY_POSTGRES_EXPORTER_PG_FULL_VERSION)' \
		--label io.k8s.display-name='$(CRUNCHY_POSTGRES_EXPORTER_IMAGE_NAME)' \
		--label io.k8s.description='$(CRUNCHY_POSTGRES_EXPORTER_DESCRIPTION)' \
		--label io.openshift.tags="postgresql,postgres,monitoring,database,crunchy" \
		--annotation org.opencontainers.image.authors='$(CRUNCHY_POSTGRES_EXPORTER_MAINTAINER)' \
		--annotation org.opencontainers.image.vendor='$(CRUNCHY_POSTGRES_EXPORTER_MAINTAINER)' \
		--annotation org.opencontainers.image.created='$(CRUNCHY_POSTGRES_EXPORTER_IMAGE_TIMESTAMP)' \
		--annotation org.opencontainers.image.description='$(CRUNCHY_POSTGRES_EXPORTER_DESCRIPTION)' \
		--annotation org.opencontainers.image.revision='$(CRUNCHY_POSTGRES_EXPORTER_IMAGE_REVISION)' \
		--annotation org.opencontainers.image.title='$(CRUNCHY_POSTGRES_EXPORTER_SUMMARY)' \
		--annotation org.opencontainers.image.url='$(CRUNCHY_POSTGRES_EXPORTER_URL)' \
		$(if $(PGO_VERSION),$(strip \
			--label release='$(PGO_VERSION)' \
			--label version='$(PGO_VERSION)' \
			--annotation org.opencontainers.image.version='$(PGO_VERSION)' \
		)) \
		--file $< --format docker --layers .

.PHONY: build-postgres-operator-image
build-postgres-operator-image: ## Build the postgres-operator image
build-postgres-operator-image: PGO_IMAGE_REVISION := $(shell git rev-parse HEAD)
build-postgres-operator-image: PGO_IMAGE_TIMESTAMP := $(shell date -u +%FT%TZ)
build-postgres-operator-image: build-postgres-operator
build-postgres-operator-image: build/postgres-operator/Dockerfile
	$(if $(shell (echo 'buildah version 1.24'; $(word 1,$(BUILDAH_BUILD)) --version) | sort -Vc 2>&1), \
		$(warning WARNING: old buildah does not invalidate its cache for changed labels: \
			https://github.com/containers/buildah/issues/3517))
	$(if $(IMAGE_TAG),,	$(error missing IMAGE_TAG))
	$(BUILDAH_BUILD) \
		--tag $(BUILDAH_TRANSPORT)$(PGO_IMAGE_PREFIX)/$(PGO_IMAGE_NAME):$(IMAGE_TAG) \
		--label name='$(PGO_IMAGE_NAME)' \
		--label build-date='$(PGO_IMAGE_TIMESTAMP)' \
		--label description='$(PGO_IMAGE_DESCRIPTION)' \
		--label maintainer='$(PGO_IMAGE_MAINTAINER)' \
		--label summary='$(PGO_IMAGE_SUMMARY)' \
		--label url='$(PGO_IMAGE_URL)' \
		--label vcs-ref='$(PGO_IMAGE_REVISION)' \
		--label vendor='$(PGO_IMAGE_MAINTAINER)' \
		--label io.k8s.display-name='$(PGO_IMAGE_NAME)' \
		--label io.k8s.description='$(PGO_IMAGE_DESCRIPTION)' \
		--label io.openshift.tags="postgresql,postgres,sql,nosql,crunchy" \
		--annotation org.opencontainers.image.authors='$(PGO_IMAGE_MAINTAINER)' \
		--annotation org.opencontainers.image.vendor='$(PGO_IMAGE_MAINTAINER)' \
		--annotation org.opencontainers.image.created='$(PGO_IMAGE_TIMESTAMP)' \
		--annotation org.opencontainers.image.description='$(PGO_IMAGE_DESCRIPTION)' \
		--annotation org.opencontainers.image.revision='$(PGO_IMAGE_REVISION)' \
		--annotation org.opencontainers.image.title='$(PGO_IMAGE_SUMMARY)' \
		--annotation org.opencontainers.image.url='$(PGO_IMAGE_URL)' \
		$(if $(PGO_VERSION),$(strip \
			--label release='$(PGO_VERSION)' \
			--label version='$(PGO_VERSION)' \
			--annotation org.opencontainers.image.version='$(PGO_VERSION)' \
		)) \
		--file $< --format docker --layers .

##@ Test
.PHONY: check
check: ## Run basic go tests with coverage output
	$(GO_TEST) -cover ./...

# Available versions: curl -s 'https://storage.googleapis.com/kubebuilder-tools/' | grep -o '<Key>[^<]*</Key>'
# - KUBEBUILDER_ATTACH_CONTROL_PLANE_OUTPUT=true
.PHONY: check-envtest
check-envtest: ## Run check using envtest and a mock kube api
check-envtest: ENVTEST_USE = hack/tools/setup-envtest --bin-dir=$(CURDIR)/hack/tools/envtest use $(ENVTEST_K8S_VERSION)
check-envtest: SHELL = bash
check-envtest:
	GOBIN='$(CURDIR)/hack/tools' $(GO) install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
	@$(ENVTEST_USE) --print=overview && echo
	source <($(ENVTEST_USE) --print=env) && IVYO_NAMESPACE="ivory-operator" $(GO_TEST) -count=1 -cover -tags=envtest ./...

# The "PGO_TEST_TIMEOUT_SCALE" environment variable (default: 1) can be set to a
# positive number that extends test timeouts. The following runs tests with 
# timeouts that are 20% longer than normal:
# make check-envtest-existing PGO_TEST_TIMEOUT_SCALE=1.2
.PHONY: check-envtest-existing
check-envtest-existing: ## Run check using envtest and an existing kube api
check-envtest-existing: createnamespaces
	kubectl apply --server-side -k ./config/dev
	USE_EXISTING_CLUSTER=true IVYO_NAMESPACE="ivory-operator" $(GO_TEST) -count=1 -cover -p=1 -tags=envtest ./...
	kubectl delete -k ./config/dev

# Expects operator to be running
.PHONY: check-kuttl
check-kuttl: ## Run kuttl end-to-end tests
check-kuttl: ## example command: make check-kuttl KUTTL_TEST='
	${KUTTL_TEST} \
		--config testing/kuttl/kuttl-test.yaml

.PHONY: generate-kuttl
generate-kuttl: export KUTTL_PG_UPGRADE_FROM_VERSION ?= 14
generate-kuttl: export KUTTL_PG_UPGRADE_TO_VERSION ?= 15
generate-kuttl: export KUTTL_PG_VERSION ?= 15
generate-kuttl: export KUTTL_POSTGIS_VERSION ?= 3.3
generate-kuttl: export KUTTL_PSQL_IMAGE ?= registry.developers.crunchydata.com/crunchydata/crunchy-postgres:ubi8-15.2-0
generate-kuttl: ## Generate kuttl tests
	[ ! -d testing/kuttl/e2e-generated ] || rm -r testing/kuttl/e2e-generated
	[ ! -d testing/kuttl/e2e-generated-other ] || rm -r testing/kuttl/e2e-generated-other
	bash -ceu ' \
	case $(KUTTL_PG_VERSION) in \
	15 ) export KUTTL_BITNAMI_IMAGE_TAG=15.0.0-debian-11-r4 ;; \
	14 ) export KUTTL_BITNAMI_IMAGE_TAG=14.5.0-debian-11-r37 ;; \
	13 ) export KUTTL_BITNAMI_IMAGE_TAG=13.8.0-debian-11-r39 ;; \
	12 ) export KUTTL_BITNAMI_IMAGE_TAG=12.12.0-debian-11-r40 ;; \
	11 ) export KUTTL_BITNAMI_IMAGE_TAG=11.17.0-debian-11-r39 ;; \
	esac; \
	render() { envsubst '"'"'$$KUTTL_PG_UPGRADE_FROM_VERSION $$KUTTL_PG_UPGRADE_TO_VERSION $$KUTTL_PG_VERSION $$KUTTL_POSTGIS_VERSION $$KUTTL_PSQL_IMAGE $$KUTTL_BITNAMI_IMAGE_TAG'"'"'; }; \
	while [ $$# -gt 0 ]; do \
		source="$${1}" target="$${1/e2e/e2e-generated}"; \
		mkdir -p "$${target%/*}"; render < "$${source}" > "$${target}"; \
		shift; \
	done' - testing/kuttl/e2e/*/*.yaml testing/kuttl/e2e-other/*/*.yaml

##@ Generate

.PHONY: check-generate
check-generate: ## Check crd, crd-docs, deepcopy functions, and rbac generation
check-generate: generate-crd
check-generate: generate-deepcopy
check-generate: generate-rbac
	git diff --exit-code -- config/crd
	git diff --exit-code -- config/rbac
	git diff --exit-code -- pkg/apis

.PHONY: generate
generate: ## Generate crd, crd-docs, deepcopy functions, and rbac
generate: generate-crd
generate: generate-crd-docs
generate: generate-deepcopy
generate: generate-rbac

.PHONY: generate-crd
generate-crd: ## Generate crd
	GOBIN='$(CURDIR)/hack/tools' ./hack/controller-generator.sh \
		crd:crdVersions='v1' \
		paths='./pkg/apis/...' \
		output:dir='build/crd/postgresclusters/generated' # build/crd/{plural}/generated/{group}_{plural}.yaml
	@
	GOBIN='$(CURDIR)/hack/tools' ./hack/controller-generator.sh \
		crd:crdVersions='v1' \
		paths='./pkg/apis/...' \
		output:dir='build/crd/pgupgrades/generated' # build/crd/{plural}/generated/{group}_{plural}.yaml
	@
	kubectl kustomize ./build/crd/postgresclusters > ./config/crd/bases/ivory-operator.highgo.com_ivoryclusters.yaml
	kubectl kustomize ./build/crd/pgupgrades > ./config/crd/bases/ivory-operator.highgo.com_ivyupgrades.yaml

.PHONY: generate-crd-docs
generate-crd-docs: ## Generate crd-docs
	GOBIN='$(CURDIR)/hack/tools' $(GO) install fybrik.io/crdoc@v0.5.2
	./hack/tools/crdoc \
		--resources ./config/crd/bases \
		--template ./hack/api-template.tmpl \
		--output ./docs/content/references/crd.md

.PHONY: generate-deepcopy
generate-deepcopy: ## Generate deepcopy functions
	GOBIN='$(CURDIR)/hack/tools' ./hack/controller-generator.sh \
		object:headerFile='hack/boilerplate.go.txt' \
		paths='./pkg/apis/ivory-operator.highgo.com/...'

.PHONY: generate-rbac
generate-rbac: ## Generate rbac
	GOBIN='$(CURDIR)/hack/tools' ./hack/generate-rbac.sh \
		'./internal/...' 'config/rbac'

##@ Release

.PHONY: license licenses
license: licenses
licenses: ## Aggregate license files
	./bin/license_aggregator.sh ./cmd/...

.PHONY: release-postgres-operator-image release-postgres-operator-image-labels
release-postgres-operator-image: ## Build the postgres-operator image and all its prerequisites
release-postgres-operator-image: release-postgres-operator-image-labels
release-postgres-operator-image: licenses
release-postgres-operator-image: build-postgres-operator-image
release-postgres-operator-image-labels:
	$(if $(PGO_IMAGE_DESCRIPTION),,	$(error missing PGO_IMAGE_DESCRIPTION))
	$(if $(PGO_IMAGE_MAINTAINER),, 	$(error missing PGO_IMAGE_MAINTAINER))
	$(if $(PGO_IMAGE_NAME),,       	$(error missing PGO_IMAGE_NAME))
	$(if $(PGO_IMAGE_SUMMARY),,    	$(error missing PGO_IMAGE_SUMMARY))
	$(if $(PGO_VERSION),,			$(error missing PGO_VERSION))

.PHONY: release-crunchy-postgres-exporter-image release-crunchy-postgres-exporter-image-labels
release-crunchy-postgres-exporter-image: ## Build the postgres-operator image and all its prerequisites
release-crunchy-postgres-exporter-image: release-crunchy-postgres-exporter-image-labels
release-crunchy-postgres-exporter-image: licenses
release-crunchy-postgres-exporter-image: build-postgres-operator-image
release-crunchy-postgres-exporter-image-labels:
	$(if $(PGO_IMAGE_DESCRIPTION),,	$(error missing PGO_IMAGE_DESCRIPTION))
	$(if $(PGO_IMAGE_MAINTAINER),, 	$(error missing PGO_IMAGE_MAINTAINER))
	$(if $(PGO_IMAGE_NAME),,       	$(error missing PGO_IMAGE_NAME))
	$(if $(PGO_IMAGE_SUMMARY),,    	$(error missing PGO_IMAGE_SUMMARY))
	$(if $(PGO_VERSION),,			$(error missing PGO_VERSION))
