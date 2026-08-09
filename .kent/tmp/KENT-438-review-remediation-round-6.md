# KENT-438 review remediation round 6

## Adjudication

- [x] The inspection mutation is a valid implementation/ownership regression:
  diagnostic resolution must remain read-only and outside the Task writer.
- [x] The repeated broad decomposition finding remains rejected for KENT-438
  by Nikita's explicit round-4 choice; KENT-458 owns that refactor.

## Remediation

- [x] Add a red persisted-inspection test proving missing provenance remains
  absent after diagnostic inspection.
- [x] Split read-only Session context resolution from explicit activation-owned
  ensure while sharing one structural resolver and exact association logic.
- [x] Route retained-Session activation to the ensure operation under the Task
  writer; leave inspection and diagnostics on the read-only resolver.
- [x] Run focused regressions, relevant full tests, compile sweep, metadata
  guards, final build, `git diff --check`, commit, and complete.
