# svg-proto - driving the work
#
# `make help` lists every target. Everything here runs offline with nothing but
# a Go toolchain; the external render oracle is optional and only needed for
# fidelity measurements.
#
# The measurement targets are the important ones. Nothing in this project is
# allowed to be believed without a number, so `make bench` is the default path,
# not an afterthought.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO           ?= go
GOBIN        := $(CURDIR)/bin
BINARIES     := svgstat svgimg
MODULE       := github.com/elfeo/svg-proto
CORPUS       := testdata/corpus
ORACLE       ?= inkscape
ORACLE_ALT   ?= resvg
BENCH_MARKS  := docs/benchmarks.md
CORPUS_BENCH := docs/corpus-bench.md
CORPUS_TRACE := testdata/corpus/.trace
DEMO         := testdata/demo

# Optional tools: detected, never required. A missing linter must not be able to
# fail a build, and a missing oracle must degrade the measurement rather than
# crash it.
HAVE_LINT    := $(shell command -v golangci-lint 2>/dev/null)
HAVE_STATIC  := $(shell command -v staticcheck 2>/dev/null)
HAVE_INK     := $(shell command -v $(ORACLE) 2>/dev/null)
HAVE_ALT     := $(shell command -v $(ORACLE_ALT) 2>/dev/null)
HAVE_MAGICK  := $(shell command -v magick 2>/dev/null || command -v convert 2>/dev/null)

GO_SOURCES   := $(shell find . -name '*.go' -not -path './.git/*' 2>/dev/null)
GO_PKGS      := $(shell $(GO) list ./... 2>/dev/null)
MODFILE      := go.mod

.PHONY: help
help: ## Show this help
	@echo "svg-proto"
	@echo
	@grep -hE '^[a-zA-Z0-9_.-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
	@echo
	@echo "variables: GO=$(GO) ORACLE=$(ORACLE) CORPUS=$(CORPUS)"
	@printf 'detected:  lint=%s staticcheck=%s oracle=%s magick=%s\n' \
		"$$([ -n '$(HAVE_LINT)' ] && echo yes || echo no)" \
		"$$([ -n '$(HAVE_STATIC)' ] && echo yes || echo no)" \
		"$$([ -n '$(HAVE_INK)$(HAVE_ALT)' ] && echo yes || echo no)" \
		"$$([ -n '$(HAVE_MAGICK)' ] && echo yes || echo no)"

# ---------------------------------------------------------------------------
# build
# ---------------------------------------------------------------------------

.PHONY: all
all: fmt vet test build ## Format, vet, test and build everything

.PHONY: build
build: $(addprefix $(GOBIN)/,$(BINARIES)) ## Compile the binaries into ./bin

$(GOBIN)/%: $(GO_SOURCES) $(MODFILE)
	@mkdir -p $(GOBIN)
	$(GO) build -trimpath -o $@ ./cmd/$*

.PHONY: install
install: ## Install the binaries into GOBIN
	$(GO) install ./cmd/...

# ---------------------------------------------------------------------------
# correctness
# ---------------------------------------------------------------------------

.PHONY: test
test: ## Run the tests
	$(GO) test ./...

.PHONY: test-race
test-race: ## Run the tests under the race detector
	$(GO) test -race ./...

.PHONY: test-v
test-v: ## Run the tests verbosely
	$(GO) test -v ./...

.PHONY: cover
cover: ## Run the tests and open a coverage summary
	$(GO) test -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out | tail -20
	@echo "coverage.out: go tool cover -html=coverage.out"

.PHONY: bench
bench: ## Run the Go benchmarks
	$(GO) test -run='^$$' -bench=. -benchmem ./...

.PHONY: fuzz
fuzz: ## Fuzz the SVG parser (Ctrl-C to stop)
	$(GO) test -run='^$$' -fuzz=FuzzAnalyze -fuzztime=60s ./internal/svgstat/

# ---------------------------------------------------------------------------
# hygiene
# ---------------------------------------------------------------------------

.PHONY: fmt
fmt: ## Rewrite the sources with gofmt
	$(GO) fmt ./...

.PHONY: fmt-check
fmt-check: ## Fail if the sources are not gofmt-clean
	@unformatted=$$(gofmt -l . 2>/dev/null); \
	if [ -n "$$unformatted" ]; then \
		echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: lint
lint: ## Run golangci-lint and staticcheck, skipping whichever is absent
	@ran=0; \
	if [ -n '$(HAVE_LINT)' ]; then ran=1; golangci-lint run ./...; \
	else echo "skip: golangci-lint not installed"; fi; \
	if [ -n '$(HAVE_STATIC)' ]; then ran=1; staticcheck ./...; \
	else echo "skip: staticcheck not installed"; fi; \
	if [ $$ran -eq 0 ]; then echo "no linter available; gofmt-check and vet are the floor"; fi

.PHONY: tidy
tidy: ## Tidy go.mod
	$(GO) mod tidy

.PHONY: tidy-check
tidy-check: ## Fail if go.mod is not tidy
	@cp $(MODFILE) $(MODFILE).bck; \
	cp go.sum go.sum.bck 2>/dev/null || true; \
	$(GO) mod tidy; \
	if ! diff -q $(MODFILE) $(MODFILE).bck >/dev/null; then \
		echo "go.mod is not tidy"; diff -u $(MODFILE).bck $(MODFILE) || true; \
		mv $(MODFILE).bck $(MODFILE); exit 1; \
	fi; \
	mv $(MODFILE).bck $(MODFILE); rm -f $(MODFILE).bck go.sum.bck; \
	echo "go.mod is tidy"

.PHONY: ci
ci: fmt-check vet tidy-check test-race ## Everything CI should run

# ---------------------------------------------------------------------------
# measurement
# ---------------------------------------------------------------------------

.PHONY: oracle-check
oracle-check: ## Report whether a render oracle is available
	@if [ -n '$(HAVE_INK)' ]; then \
		echo "oracle: $(ORACLE) ($(shell $(ORACLE) --version 2>/dev/null | head -1))"; \
	elif [ -n '$(HAVE_ALT)' ]; then \
		echo "oracle: $(ORACLE_ALT) (fall back with: make ORACLE=$(ORACLE_ALT) ...)"; \
	else \
		echo "oracle: NONE. Cost metrics still work; fidelity metrics (--ref) do not."; \
		echo "install resvg (preferred: it is the reference renderer) or inkscape."; \
	fi

.PHONY: demo
demo: $(GOBIN)/svgstat ## Build a small SVG + matching raster and measure it
	@mkdir -p $(DEMO)
	@if [ -n '$(HAVE_MAGICK)' ]; then \
		$(HAVE_MAGICK) -size 200x120 xc:white -fill '#2b6cb0' \
			-draw 'polygon 20,100 60,100 40,20' \
			-fill '#e53e3e' -draw 'rectangle 140,20 180,100' -depth 8 $(DEMO)/ref.png; \
		printf '%s' '<svg xmlns="http://www.w3.org/2000/svg" width="200" height="120" viewBox="0 0 200 120"><path fill="#2b6cb0" d="M20 100 60 100 40 20Z"/><path fill="#e53e3e" d="M140 20h40v80h-40Z"/></svg>' > $(DEMO)/ref.svg; \
		$(GOBIN)/svgstat $(DEMO)/ref.svg --ref $(DEMO)/ref.png --oracle $(ORACLE); \
	else \
		echo "skip: neither magick nor convert found, cannot generate the demo raster"; \
	fi

.PHONY: stats
stats: $(GOBIN)/svgstat ## Measure one SVG: make stats FILE=path/to/file.svg [REF=raster.png]
	@test -n '$(FILE)' || { echo "usage: make stats FILE=file.svg [REF=ref.png]"; exit 2; }
	@if [ -n '$(REF)' ]; then \
		$(GOBIN)/svgstat '$(FILE)' --ref '$(REF)' --oracle $(ORACLE); \
	else \
		$(GOBIN)/svgstat '$(FILE)'; \
	fi

# The corpus rasters are *generated* from the corpus SVGs, not collected. That
# matters: the SVG is the ground truth, so a raster that disagreed with it would
# be measuring the wrong thing, and there would be no way to tell whether the
# tracer or the reference was at fault. Rendering is reproducible and cheap, so
# the PNGs are build output in every sense except that they are checked in.
#
# No --export-width/--export-height: inkscape then renders at the SVG's intrinsic
# size, which is exactly the viewBox size, so 1 user unit is 1 pixel and the
# tracer sees the shape at its native resolution.
.PHONY: corpus-render
corpus-render: ## Render every testdata/corpus/*.svg to its matching .png via the oracle
	@if [ -z "$(HAVE_INK)$(HAVE_ALT)" ]; then \
		echo "no render oracle; install $(ORACLE) or $(ORACLE_ALT)"; exit 1; \
	fi
	@oracle='$(ORACLE)'; [ -n "$(HAVE_INK)" ] || oracle='$(ORACLE_ALT)'; \
	n=0; \
	for svg in $(CORPUS)/*/*.svg; do \
		[ -e "$$svg" ] || continue; \
		png="$${svg%.svg}.png"; \
		printf '  %-46s ' "$$svg"; \
		$$oracle --export-type=png --export-filename="$$png" "$$svg" 2>/dev/null; \
		geom=$$(magick identify -format '%wx%h' "$$png" 2>/dev/null || echo '?'); \
		echo "$$geom"; \
		n=$$((n+1)); \
	done; \
	echo "rendered $$n rasters"

# The corpus is laid out as testdata/corpus/<category>/*.{svg,png}, where the
# matching raster shares the base name. The category is the directory name, so
# aggregating by category needs no manifest.
CORPUS_CATS := logo icon ui screenshot lineart pixelart photo alpha anim

.PHONY: corpus
corpus: $(GOBIN)/svgstat ## Report corpus coverage by category
	@if [ ! -d '$(CORPUS)' ]; then \
		echo "no corpus at $(CORPUS) yet - this is the remaining Phase 0 deliverable"; \
		echo "expected layout:"; \
		for c in $(CORPUS_CATS); do echo "  $(CORPUS)/$$c/name.svg + name.png"; done; \
		exit 1; \
	fi
	@printf 'category      svg  with-ref  missing-ref\n'
	@for c in $(CORPUS_CATS); do \
		n=0; w=0; m=0; \
		for f in '$(CORPUS)'/"$$c"/*.svg; do \
			[ -e "$$f" ] || continue; \
			n=$$((n+1)); \
			if [ -e "$${f%.svg}.png" ]; then w=$$((w+1)); else m=$$((m+1)); fi; \
		done; \
		if [ "$$n" -gt 0 ]; then printf '%-13s %4d  %9d  %12d\n' "$$c" "$$n" "$$w" "$$m"; fi; \
	done; \
	echo; \
	echo "fidelity measurement needs the oracle: make oracle-check"

# The reference images we are allowed to measure against. Deliberately not the
# corpus: the corpus is third-party content for the long run, these are the
# project's own two test images, so the Phase 1 numbers have a fixed, reviewable
# definition.
ASSETS_PNG  := $(wildcard assets/png/*.png)
ASSETS_SVG  := assets/svg
# The three reference encoders, plus the tracer that has to beat them. They are
# measured through the same harness on purpose: a tracer scored on its own terms
# is not being scored.
#
# The node budget is applied to trace only. The baselines run uncapped, because a
# reference that has been clipped to look closer to the tracer is not a
# reference any more, and docs/benchmarks.md records them uncapped.
BASELINE_MODES := embed grid runlength
ALL_MODES     := $(BASELINE_MODES) trace
BASELINE_K    ?= 16
TRACE_MAX_NODES ?= 20000

.PHONY: convert
convert: $(GOBIN)/svgimg ## Re-encode assets/png into assets/svg with every encoder
	@test -n '$(ASSETS_PNG)' || { echo "no images in assets/png"; exit 1; }
	@mkdir -p $(ASSETS_SVG)
	@for png in $(ASSETS_PNG); do \
		name=$$(basename "$$png" .png); \
		for m in $(ALL_MODES); do \
			printf '%-28s ' "$$name / $$m"; \
			extra=''; \
			if [ "$$m" = trace ]; then extra='-max-nodes $(TRACE_MAX_NODES)'; fi; \
			$(GOBIN)/svgimg -in "$$png" -mode "$$m" -k $(BASELINE_K) $$extra \
				-out '$(ASSETS_SVG)'/"$$name-$$m.svg" 2>&1 \
				| awk '/^  (shapes|bytes|vs source bytes)/ {printf "%s ", $$0}'; \
			echo; \
		done; \
	done

# This re-encodes before it measures, on purpose. It used to depend only on
# svgstat, which meant it measured whatever SVG happened to be lying in
# assets/svg: change the encoder, re-run the benchmark, and you get the previous
# encoder's numbers with no error anywhere. On a project whose only rule is that
# nothing is believed without a measurement, silently stale numbers are the worst
# failure mode available.
.PHONY: bench-baselines
bench-baselines: convert $(GOBIN)/svgstat ## Re-encode then measure assets/svg against assets/png into docs/benchmarks.md
	@mkdir -p $(dir $(BENCH_MARKS))
	@{ \
		echo '# Benchmarks'; \
		echo; \
		echo '<!-- generated by make bench-baselines; do not edit by hand -->'; \
		echo; \
		echo 'Cost here is **nodes**, not bytes: bytes are transport, and gzip handles'; \
		echo 'them (D6). Fidelity comes from rendering the SVG through the oracle and'; \
		echo 'comparing against the source raster.'; \
		echo; \
	printf '| encoder | image | bytes | gzip | elements | paths | points | fills | vs raster | ssim | psnr | dE OKLab | score |\n'; \
	printf '|---|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|\n'; \
		for png in $(ASSETS_PNG); do \
			name=$$(basename "$$png" .png); \
			for m in $(ALL_MODES); do \
				svg='$(ASSETS_SVG)'/"$$name-$$m.svg"; \
				[ -e "$$svg" ] || continue; \
				$(GOBIN)/svgstat "$$svg" --ref "$$png" --oracle $(ORACLE) \
					-format row -category "$$m" -name "$$name" 2>/dev/null || \
				$(GOBIN)/svgstat "$$svg" -format row -category "$$m" -name "$$name (no fidelity)"; \
			done; \
		done; \
	} > $(BENCH_MARKS)
	@echo "wrote $(BENCH_MARKS)"
	@grep -c '^|' $(BENCH_MARKS) | xargs -I{} echo "{} table lines"

# Measures what the tracer *produces* against the corpus rasters, which is the
# question that matters. `bench-table` below measures the hand-authored reference
# SVGs, so it measures the oracle's fidelity and the harness, not the encoder.
# Both numbers are wanted: a corpus whose own SVG does not round-trip to its raster
# is a broken reference, and that has to be visible rather than assumed.
#
# One row per corpus file, plus the two reference encoders on the same inputs, so
# the tracer is scored against the same raster the baselines see.
CORPUS_TOL      ?= 0.25
CORPUS_MAX_NODES ?= 20000
# The backdrop corpus-bench composites the source over. It must match svgstat's -bg,
# which defaults to white: a different pair measures a render against a reference
# prepared by another rule.
CORPUS_BG       ?= white

# How far a fitted curve may sit from the points it was fitted from, in px. See
# curvefit.Options. This is a different quantity from CORPUS_TOL, which simplifies
# the traced staircase before any fitting happens: the two budgets apply in
# sequence, so the shape a curve is fitted from is already the simplified one.
CORPUS_CURVE_TOL ?= 0.4

.PHONY: corpus-bench
# The trace-bg row is trace with the source composited over the measurement's own
# backdrop. It shows what flattening buys instead of leaving it to be rediscovered,
# and on an opaque image it duplicates trace exactly, which is the point: the row
# only differs where alpha exists. A '#' comment cannot live inside the shell block
# below, because on a backslash-continued line it eats the newline and closes the
# for loop.
corpus-bench: $(GOBIN)/svgimg $(GOBIN)/svgstat ## Trace every corpus raster and measure the output against it
	@# Wipe the trace directory first. A corpus file that has been deleted leaves
	@# its trace behind otherwise, and a stale SVG sitting next to a live one is
	@# how bench-baselines came to measure assets that no longer existed.
	@rm -rf $(CORPUS_TRACE)
	@mkdir -p $(dir $(CORPUS_BENCH)) $(CORPUS_TRACE)
	@{ \
		echo '# Corpus benchmark'; \
		echo; \
		echo '<!-- generated by make corpus-bench; do not edit by hand -->'; \
		echo; \
		printf '| encoder | file | bytes | gzip | elements | paths | points | fills | vs raster | ssim | psnr | dE OKLab | score |\n'; \
		printf '|---|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|\n'; \
		for png in $(CORPUS)/*/*.png; do \
			[ -e "$$png" ] || continue; \
			cat=$$(basename "$$(dirname "$$png")"); \
			name=$$(basename "$$png" .png); \
			for m in grid runlength trace trace-bg trace-curves; do \
				out='$(CORPUS_TRACE)'/"$$cat-$$name-$$m.svg"; \
				enc=$$m; extra=''; \
				[ "$$m" = trace ] && extra="-max-nodes $(CORPUS_MAX_NODES)"; \
				if [ "$$m" = trace-bg ]; then enc=trace; extra="-max-nodes $(CORPUS_MAX_NODES) -bg $(CORPUS_BG)"; fi; \
				if [ "$$m" = trace-curves ]; then enc=trace; extra="-max-nodes $(CORPUS_MAX_NODES) -curves -curve-tol $(CORPUS_CURVE_TOL)"; fi; \
				$(GOBIN)/svgimg -in "$$png" -mode "$$enc" -k $(BASELINE_K) -tol $(CORPUS_TOL) \
					$$extra -out "$$out" >/dev/null 2>&1 || continue; \
				$(GOBIN)/svgstat "$$out" --ref "$$png" --oracle $(ORACLE) -bg $(CORPUS_BG) \
					-format row -category "$$m" -name "$$cat/$$name" 2>/dev/null || \
				$(GOBIN)/svgstat "$$out" -format row -category "$$m" -name "$$cat/$$name (no fidelity)"; \
			done; \
		done; \
	} > $(CORPUS_BENCH)
	@echo "wrote $(CORPUS_BENCH)"
	@column -t -s'|' < $(CORPUS_BENCH)

.PHONY: bench-table
bench-table: $(GOBIN)/svgstat ## Measure every corpus SVG into docs/benchmarks.md
	@test -d '$(CORPUS)' || { echo "no corpus at $(CORPUS); see 'make corpus'"; exit 1; }
	@mkdir -p $(dir $(BENCH_MARKS))
	@{ \
		echo '# Benchmarks'; \
		echo; \
		echo '<!-- generated by make bench-table; do not edit by hand -->'; \
		echo; \
		echo 'Cost here is **nodes and segments**, not bytes: bytes are transport,'; \
		echo 'and gzip handles them (D6). Fidelity comes from rendering the SVG'; \
		echo 'through the oracle and comparing against the source raster.'; \
		echo; \
		printf '| category | file | bytes | gzip | elements | paths | points | fills | vs raster | ssim | psnr | dE OKLab | score |\n'; \
		printf '|---|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|\n'; \
		for c in $(CORPUS_CATS); do \
			for f in '$(CORPUS)'/"$$c"/*.svg; do \
				[ -e "$$f" ] || continue; \
				ref="$${f%.svg}.png"; \
				if [ -e "$$ref" ]; then \
					$(GOBIN)/svgstat "$$f" --ref "$$ref" --oracle $(ORACLE) \
						-format row -category "$$c" -name "$$(basename "$$f")" 2>/dev/null || \
					$(GOBIN)/svgstat "$$f" -format row -category "$$c" -name "$$(basename "$$f") (no fidelity)"; \
				else \
					$(GOBIN)/svgstat "$$f" -format row -category "$$c" -name "$$(basename "$$f") (no ref)"; \
				fi; \
			done; \
		done; \
	} > $(BENCH_MARKS)
	@echo "wrote $(BENCH_MARKS)"
	@grep -c '^|' $(BENCH_MARKS) | xargs -I{} echo "{} table lines"


# ---------------------------------------------------------------------------
# housekeeping
# ---------------------------------------------------------------------------

.PHONY: clean
clean: ## Remove build output and coverage files
	rm -rf $(GOBIN) coverage.out $(DEMO)
	$(GO) clean -testcache

.PHONY: ci-clean
ci-clean: clean ## Also clear the module and build caches
	$(GO) clean -modcache -cache
