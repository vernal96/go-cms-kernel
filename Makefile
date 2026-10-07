SHELL := /bin/sh

.PHONY: version

version:
	@test -n "$(VERSION)" || { printf 'Usage: make version VERSION=v0.5.0\n' >&2; exit 2; }
	@printf '%s\n' "$(VERSION)" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$$' || { printf 'VERSION must use vMAJOR.MINOR.PATCH format.\n' >&2; exit 2; }
	@if git rev-parse -q --verify "refs/tags/$(VERSION)" >/dev/null; then printf 'Tag $(VERSION) already exists.\n' >&2; exit 1; fi
	git tag -a "$(VERSION)" -m "$(VERSION)"
