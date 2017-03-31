export GO15VENDOREXPERIMENT=1

.PHONY: build
build:
	go build

.PHONY: test
test:
	go test -cover -race -v

update-license:
	@go get go.uber.org/tools/update-license
	@update-license \
		$(shell go list -json $(PACKAGES) | \
			jq -r '.Dir + "/" + (.GoFiles | .[])')
