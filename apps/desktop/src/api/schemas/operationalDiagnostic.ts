import { z } from "zod";

import { nonBlankString } from "./common";

const detailedOperationalDiagnosticCodeSchema = z.enum([
  "sleep_guard_failed",
  "prompt_history_persist_failed",
  "in_flight_clear_failed",
]);
const providerStateOperationalDiagnosticCodeSchema = z.enum([
  "provider_turn_state_invalid",
  "provider_turn_state_conflict",
]);
const optionalStepIDSchema = nonBlankString.nullish();
const diagnosticDetailSchema = z.string().refine((value) => value.trim().length > 0);

export type DetailedOperationalDiagnostic = Readonly<{
  kind: "detailed";
  code: z.output<typeof detailedOperationalDiagnosticCodeSchema>;
  stepID?: string;
  detail: string;
}>;

export type ProviderStateOperationalDiagnostic = Readonly<{
  kind: "provider_state";
  code: z.output<typeof providerStateOperationalDiagnosticCodeSchema>;
  stepID?: string;
}>;

export type OperationalDiagnostic = DetailedOperationalDiagnostic | ProviderStateOperationalDiagnostic;

const detailedOperationalDiagnosticSchema = z
  .object({
    Code: detailedOperationalDiagnosticCodeSchema,
    StepID: optionalStepIDSchema,
    Detail: diagnosticDetailSchema,
  })
  .strict()
  .transform((value): DetailedOperationalDiagnostic => ({
    kind: "detailed",
    code: value.Code,
    ...(value.StepID == null ? {} : { stepID: value.StepID }),
    detail: value.Detail,
  }));

const providerStateOperationalDiagnosticSchema = z
  .object({
    Code: providerStateOperationalDiagnosticCodeSchema,
    StepID: optionalStepIDSchema,
  })
  .strict()
  .transform((value): ProviderStateOperationalDiagnostic => ({
    kind: "provider_state",
    code: value.Code,
    ...(value.StepID == null ? {} : { stepID: value.StepID }),
  }));

export const operationalDiagnosticSchema: z.ZodType<OperationalDiagnostic> = z.union([
  detailedOperationalDiagnosticSchema,
  providerStateOperationalDiagnosticSchema,
]);

export function decodeOperationalDiagnostic(payload: unknown): OperationalDiagnostic {
  return operationalDiagnosticSchema.parse(payload);
}
