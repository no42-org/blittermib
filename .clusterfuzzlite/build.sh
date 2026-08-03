#!/bin/bash -eu
# Copyright 2026 Ronny Trommer <ronny@no42.org>
# SPDX-License-Identifier: MIT
#
# Build native Go fuzz targets into libFuzzer binaries for
# ClusterFuzzLite. compile_native_go_fuzzer imports the package under
# test, so only importable packages qualify — cmd/mib-ingest's
# FuzzNormalizeLastUpdated lives in `package main` and stays on native
# `go test -fuzz` (make fuzz) until its function is moved to an
# importable package.

cd "$SRC/blittermib"

# go-118-fuzz-build rewrites each `func Fuzz*(*testing.F)` to use its
# libFuzzer shim, so its `testing` package must resolve. Pull it into
# the module graph at build time (it never enters the committed go.mod —
# only the real code's deps do). Network is available in the OSS-Fuzz
# build; this is the documented native-Go-fuzzer step.
#
# Pinned by commit rather than floating (Scorecard Pinned-Dependencies).
# Upstream publishes no semver tags, so an unpinned `go get` means
# `@latest`, which Go resolves to the default-branch head — this commit
# (v0.0.0-20250520111509-a70c2aa677fa), so the pin is a no-op today.
# Do NOT pin the `release` tag instead: it points at a 2022 commit and
# would silently downgrade the shim by three years.
#
# Nothing bumps this automatically — Dependabot does not read `go get` in
# shell scripts — so it moves by hand when the fuzz build needs a newer
# shim.
go get github.com/AdamKorcz/go-118-fuzz-build/testing@a70c2aa677fa43583571959478decabe02a96cd6

compile_native_go_fuzzer \
  github.com/no42-org/blittermib/internal/walk \
  FuzzParse \
  fuzz_parse
