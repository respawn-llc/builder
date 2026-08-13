import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ProviderStateDiagnosticNotice } from "./ProviderStateDiagnosticNotice";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => `localized:${key}`,
  }),
}));

describe("ProviderStateDiagnosticNotice", () => {
  it.each([
    {
      code: "provider_turn_state_invalid",
      titleKey: "operationalDiagnostic.providerTurnStateInvalid.title",
      bodyKey: "operationalDiagnostic.providerTurnStateInvalid.body",
    },
    {
      code: "provider_turn_state_conflict",
      titleKey: "operationalDiagnostic.providerTurnStateConflict.title",
      bodyKey: "operationalDiagnostic.providerTurnStateConflict.body",
    },
  ] as const)("renders localized recovery guidance for $code", ({ code, titleKey, bodyKey }) => {
    const onRetry = vi.fn();
    render(
      <ProviderStateDiagnosticNotice
        diagnostic={{ kind: "provider_state", code, stepID: "private-step-id" }}
        onRetry={onRetry}
      />,
    );

    expect(screen.getByRole("status")).toHaveAttribute("data-tone", "warning");
    expect(screen.getByRole("heading", { name: `localized:${titleKey}` })).toBeVisible();
    expect(screen.getByText(`localized:${bodyKey}`)).toBeVisible();
    expect(screen.queryByText(/private-step-id/i)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "localized:operationalDiagnostic.retryAction" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });
});
