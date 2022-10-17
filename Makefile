GIT_TAG := $(shell git tag --points-at HEAD)
VERSION ?= $(shell echo $${GIT_TAG:-0.0.0} | sed s/v//g)
IMAGE ?= ghcr.io/reddec/minio-ext-operator:$(VERSION)
LOCALBIN := $(shell pwd)/.bin
CONTROLLER_GEN := $(LOCALBIN)/controller-gen

info:
	@echo $(IMAGE)

.PHONY: manifests
manifests: $(CONTROLLER_GEN) ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: run
run: manifests generate
	MINIO_URL=$(shell kubectl get nodes -o wide --no-headers | awk '{print $$6":30080"}') \
	MINIO_USER="minioadmin" \
	MINIO_PASSWORD="minioadmin" \
	MINIO_REGION="us-east-1" \
	go run ./main.go

.PHONY: install

bundle: manifests generate
	rm -rf build && mkdir build
	cp -rv config ./build/
	cd build/config/default && kustomize edit set image controller=${IMAGE}
	kustomize build build/config/default > build/minio-ext-operator.yaml
	rm -rf build/config

bundle-example: manifests generate
	rm -rf build && mkdir build
	cp -rv config ./build/
	cd build/config/default && kustomize edit set image controller=${IMAGE}
	kustomize build build/config/default > example/minio-ext-operator.yaml
	rm -rf build/config

.PHONY: bundle

install:
	goreleaser build --rm-dist --snapshot --single-target

$(CONTROLLER_GEN):
	@mkdir -p $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.9.2