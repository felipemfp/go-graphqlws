VERSION ?= $(shell git describe --tags 2>/dev/null | cut -c 2-)
TEST_FLAGS ?=
REPO_OWNER ?= $(shell cd .. && basename "$$(pwd)")

GOPATH=$(CURDIR)/../../../../
GOPATHCMD=GOPATH=$(GOPATH)

COVERDIR=$(CURDIR)/.cover
COVERAGEFILE=$(COVERDIR)/cover.out

.PHONY: test test-watch coverage coverage-ci coverage-html dep-ensure dep-update vet lint fmt

test:
	@${GOPATHCMD} ginkgo --failFast ./...

test-watch:
	@${GOPATHCMD} ginkgo watch -cover -r ./...

coverage:
	@mkdir -p $(COVERDIR)
	@${GOPATHCMD} ginkgo -r -covermode=count --cover --trace ./
	@echo "mode: count" > "${COVERAGEFILE}"
	@find . -type f -name *.coverprofile -exec grep -h -v "^mode:" {} >> "${COVERAGEFILE}" \; -exec rm -f {} \;

coverage-ci:
	@mkdir -p $(COVERDIR)
	@${GOPATHCMD} ginkgo -r -covermode=count --cover --trace ./
	@echo "mode: count" > "${COVERAGEFILE}"
	@find . -type f -name *.coverprofile -exec grep -h -v "^mode:" {} >> "${COVERAGEFILE}" \; -exec rm -f {} \;

coverage-html:
	@$(GOPATHCMD) go tool cover -html="${COVERAGEFILE}" -o .cover/report.html

dep-init:
	@$(GOPATHCMD) dep init -v

dep-ensure:
	@$(GOPATHCMD) dep ensure -v

dep-update:
	@$(GOPATHCMD) dep ensure -update -v

vet:
	@$(GOPATHCMD) go vet ./...

lint:
	@$(GOPATHCMD) golint

fmt:
	@$(GOPATHCMD) go fmt ./...
