import { describe, expect, it } from "vitest";

import { decodeOperationalDiagnostic } from "./operationalDiagnostic";

const detailedCodes = [
  "sleep_guard_failed",
  "prompt_history_persist_failed",
  "in_flight_clear_failed",
] as const;
const providerStateCodes = ["provider_turn_state_invalid", "provider_turn_state_conflict"] as const;

describe("operational diagnostic schema", () => {
  it.each(detailedCodes)("adapts detailed diagnostic %s", (code) => {
    expect(
      decodeOperationalDiagnostic({
        Code: code,
        StepID: "step-1",
        Detail: "The operation failed.",
      }),
    ).toEqual({
      kind: "detailed",
      code,
      stepID: "step-1",
      detail: "The operation failed.",
    });
  });

  it("preserves detailed diagnostic copy exactly", () => {
    expect(
      decodeOperationalDiagnostic({
        Code: "sleep_guard_failed",
        Detail: "  operating system detail  ",
      }),
    ).toMatchObject({ detail: "  operating system detail  " });
  });

  it.each(providerStateCodes)("adapts code-only provider-state diagnostic %s", (code) => {
    expect(decodeOperationalDiagnostic({ Code: code, StepID: null })).toEqual({
      kind: "provider_state",
      code,
    });
  });

  it.each([
    {},
    { Detail: "The operation failed." },
    { Code: "" },
    { Code: "future_diagnostic" },
    { Code: "sleep_guard_failed" },
    { Code: "sleep_guard_failed", Detail: "" },
    { Code: "sleep_guard_failed", Detail: " \t " },
    { Code: "sleep_guard_failed", Detail: "failure", StepID: "" },
    { Code: "provider_turn_state_invalid", Detail: "provider value" },
    { Code: "provider_turn_state_conflict", Detail: "" },
  ])("rejects invalid or mismatched payload %#", (payload) => {
    expect(() => decodeOperationalDiagnostic(payload)).toThrow();
  });
});
