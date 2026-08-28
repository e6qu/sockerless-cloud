# PLAN

The current delivery adds one shared authenticated application-observation
boundary to the three simulator binaries, publishes each at
`GET /monitoring/observation`, and then advances their common immutable release
coordinate in the owning infrastructure repository. Acceptance requires
Shauth to collect fresh healthy `e6qu.monitoring/v2` observations for AWS,
Google Cloud, and Microsoft Azure while the existing SDK, CLI, Terraform,
browser, and simulator gates remain green. Current work items live in
DO_NEXT.md; the repository snapshot lives in STATUS.md.
