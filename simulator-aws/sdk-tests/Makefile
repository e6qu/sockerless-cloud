# Auto-generated test Makefile — see docs/MAKEFILE_STANDARD.md.
# These tests live in their own Go module (not the workspace) and
# exercise the parent simulator binary against real cloud SDKs / CLIs.

GO_ENV := # This official-client suite deliberately covers every AWS service shard and
# real workload locally. Its normal runtime exceeds Go's default ten-minute
# package timeout even though CI fans the same exhaustive set out across four
# independently bounded jobs.
GO_TEST_FLAGS := -timeout 30m

REPO_ROOT_REL := ../..

include $(REPO_ROOT_REL)/make/go-lib.mk
