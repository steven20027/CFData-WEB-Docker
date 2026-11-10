IMAGE ?= cfdata-web:1.8.5
VERSION ?= v1.8.5
MIRROR ?= https://ghfast.top/

.PHONY: build run region bestcf bestcf-extract extract scan shell multiarch clean

build:
	docker build \
		--build-arg CFDATA_VERSION=$(VERSION) \
		--build-arg CFDATA_MIRROR=$(MIRROR) \
		-t $(IMAGE) .

run: build
	mkdir -p data
	docker run --rm -v "$(PWD)/config:/config:ro" -v "$(PWD)/data:/data" $(IMAGE) run

# 按地区优选，例如 make region REGION=HK MODE=bestcf
REGION ?= HK
MODE ?= bestcf
region: build
	mkdir -p data
	docker run --rm -v "$(PWD)/config:/config:ro" -v "$(PWD)/data:/data" \
	  -e CF_REGION="$(REGION)" $(IMAGE) $(MODE)

bestcf: build
	mkdir -p data
	docker run --rm -v "$(PWD)/data:/data" $(IMAGE) bestcf

bestcf-extract: build
	docker run --rm -v "$(PWD)/data:/data" $(IMAGE) bestcf-extract

extract: build
	docker run --rm -v "$(PWD)/config:/config:ro" $(IMAGE) extract

scan: build
	docker run --rm -v "$(PWD)/data:/data" $(IMAGE) scan

shell: build
	docker run --rm -it -v "$(PWD)/config:/config:ro" -v "$(PWD)/data:/data" $(IMAGE) shell

multiarch:
	docker buildx build --platform linux/amd64,linux/arm64 \
		--build-arg CFDATA_VERSION=$(VERSION) \
		--build-arg CFDATA_MIRROR=$(MIRROR) \
		-t $(IMAGE) --push .

clean:
	rm -rf data
