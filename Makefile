IMAGE_NAME ?= vault-unseal-controller
IMAGE_TAG ?= latest
REGISTRY ?= your-registry.com

.PHONY: build push deploy clean

build:
	docker build -t $(IMAGE_NAME):$(IMAGE_TAG) .

push: build
	docker tag $(IMAGE_NAME):$(IMAGE_TAG) $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)
	docker push $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

deploy:
	kubectl apply -f deploy/serviceaccount.yaml
	kubectl apply -f deploy/clusterrole.yaml
	kubectl apply -f deploy/clusterrolebinding.yaml
	kubectl apply -f deploy/imagepullsecret.yaml
	kubectl apply -f deploy/secret-template.yaml
	kubectl apply -f deploy/deployment.yaml

clean:
	kubectl delete -f deploy/deployment.yaml --ignore-not-found
	kubectl delete -f deploy/secret-template.yaml --ignore-not-found
	kubectl delete -f deploy/imagepullsecret.yaml --ignore-not-found
	kubectl delete -f deploy/clusterrolebinding.yaml --ignore-not-found
	kubectl delete -f deploy/clusterrole.yaml --ignore-not-found
	kubectl delete -f deploy/serviceaccount.yaml --ignore-not-found
