import { nativeValue } from "@app/native-bridge";
import type { VendorValue } from "@xyflow/react";
import { apiCompositionValue } from "@/api/composition";
import { apiValue } from "@/api";
import { facadeValue } from "@/app-facade";
import { featureValue } from "@/features/alpha";
import { i18nValue } from "@/i18n";
import { sharedValue } from "@/shared/alpha";
import { uiValue } from "@/ui";

export const testHarnessValues = [
  apiCompositionValue,
  apiValue,
  facadeValue,
  featureValue,
  i18nValue,
  nativeValue,
  sharedValue,
  uiValue,
] satisfies readonly string[];

export type TestHarnessVendorValue = VendorValue;
