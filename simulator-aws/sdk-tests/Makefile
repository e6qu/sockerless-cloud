# Auto-generated test Makefile — see docs/MAKEFILE_STANDARD.md.
# These tests live in their own Go module (not the workspace) and
# exercise the parent simulator binary against real cloud SDKs / CLIs.

GO_ENV :=
# This official-client suite covers every AWS service shard and real workload
# locally, which runs past Go's default ten-minute package timeout; CI fans the
# same set out across independently bounded jobs.
GO_TEST_FLAGS := -timeout 30m

REPO_ROOT_REL := ../..

include $(REPO_ROOT_REL)/make/go-lib.mk
