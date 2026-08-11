# DO NEXT

1. After the first push: tag `realexec/v0.1.0`, `ui-auth/v0.1.0`,
   `testutil/v0.1.0`; switch the three simulator modules' requires to those
   tags (drop the bootstrap placeholder versions), `go mod tidy`, commit, and
   tag `simulator-aws/v0.1.0`, `simulator-gcp/v0.1.0`,
   `simulator-azure/v0.1.0`.
2. Verify `go install github.com/e6qu/sockerless-cloud/simulator-<cloud>@v0.1.0`
   works from a clean module cache for all three clouds.
3. Watch the first CI run on GitHub Actions; fix anything the Linux runners
   surface that macOS could not exercise locally (Docker-harness suites,
   Firecracker/KVM realexec tests).
