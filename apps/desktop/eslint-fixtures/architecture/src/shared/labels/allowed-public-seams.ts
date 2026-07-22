import { apiValue } from "@/api";
import { facadeValue } from "@/app-facade";
import { uiValue } from "@/ui";

export const allowedLabelCapabilityValues = [apiValue, facadeValue, uiValue] satisfies readonly string[];
