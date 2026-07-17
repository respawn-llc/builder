import { nativeValue } from "@app/native-bridge";
import { apiValue } from "@/api";
import { uiValue } from "@/ui";

export const allowedFacadeValues = [apiValue, nativeValue, uiValue] satisfies readonly string[];
