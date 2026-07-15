# KENT-243 live QA

Build Kent, then run the isolated PTY instrument:

```sh
./scripts/build.sh --output ./bin/kent
go run ./.kent/qa/KENT-243 ./bin/kent
```

The instrument creates and removes an isolated persistence root, workspace, `config.toml`, and skill fixture for each scenario. It verifies:

- a globally disabled skill marker is absent from the parent model request;
- `[subagents.qa.skills].enabled = true` restores the marker for that role;
- `/status` opens against the disabled global policy and prints the captured 80×32 screen for neutral-state and no-enumeration review.

The shared PTY cleanup supervisor terminates the client, Kent server, and model stub and removes each temporary run root.
