# KENT-438 review remediation round 21

- [x] Verify the reported prompt/publication/authority lock cycle.
- [x] Add a regression proving resolution publication does not retain the prompt lock.
- [x] Serialize prompt resolution ownership while publishing outside the prompt lock.
- [x] Run focused race tests, affected suites, build, and diff-cap checks.
- [x] Commit, push, resolve the thread, and return PR #724 to the watcher.
