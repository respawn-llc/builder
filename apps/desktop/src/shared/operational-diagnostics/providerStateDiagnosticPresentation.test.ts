import { describe, expect, it, vi } from "vitest";

import type { ProviderStateOperationalDiagnostic } from "@/api";
import { providerStateDiagnosticPresentation } from "./providerStateDiagnosticPresentation";

describe("provider-state diagnostic presentation", () => {
  it.each([
    {
      code: "provider_turn_state_invalid",
      keys: [
        "operationalDiagnostic.providerTurnStateInvalid.title",
        "operationalDiagnostic.providerTurnStateInvalid.body",
        "operationalDiagnostic.retryAction",
      ],
    },
    {
      code: "provider_turn_state_conflict",
      keys: [
        "operationalDiagnostic.providerTurnStateConflict.title",
        "operationalDiagnostic.providerTurnStateConflict.body",
        "operationalDiagnostic.retryAction",
      ],
    },
  ] as const)("maps $code to localized warning guidance", ({ code, keys }) => {
    const translate = vi.fn((key: string) => `localized:${key}`);
    const presentation = providerStateDiagnosticPresentation({ kind: "provider_state", code }, translate);

    expect(translate.mock.calls.map(([key]) => key)).toEqual(keys);
    expect(presentation.title).toBe(`localized:${keys[0]}`);
    expect(presentation.body).toBe(`localized:${keys[1]}`);
    expect(presentation.actionLabel).toBe(`localized:${keys[2]}`);
  });

  it.each(["provider_turn_state_invalid", "provider_turn_state_conflict"] as const)(
    "contains recovery guidance without exposing provider values for %s",
    (code) => {
      const diagnostic: ProviderStateOperationalDiagnostic = {
        kind: "provider_state",
        code,
        stepID: "step-1",
      };
      const presentation = providerStateDiagnosticPresentation(diagnostic, (key) => `localized:${key}`);

      expect(presentation.actionLabel).toBe("localized:operationalDiagnostic.retryAction");
      expect(presentation.body).toContain("localized:operationalDiagnostic.");
      expect(JSON.stringify(presentation)).not.toContain("step-1");
      expect(presentation).not.toHaveProperty("id");
      expect(presentation).not.toHaveProperty("onAction");
    },
  );
});
