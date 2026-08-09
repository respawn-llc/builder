# KENT-438 review remediation round 15

- [x] Add failing coverage for selected-Task lifecycle captures.
- [x] Make selected captures resolve only supplied Task IDs from one capture.
- [x] Add failing coverage proving immutable roots retain no Question payload bytes.
- [x] Move paged Question payloads into the owner-exclusive ephemeral SQLite read projection.
- [x] Preserve serialized projection/root publication and projection-failure/root-unchanged behavior.
- [x] Verify startup rehydration still uses the existing typed prompt publication path.
- [x] Run affected suites, architecture guards, race coverage, and required build.
- [x] Confirm cumulative net correction stays at or below ~1,650 LoC.
- [x] Commit and push the round atomically.
- [x] Reply to and resolve both review threads.
- [x] Return PR #724 to the watcher.
