import { useState } from "react";

type RetainedQueryData<TData, TScope> = Readonly<{
  data: TData;
  scope: TScope;
}>;

export function useRetainedQueryData<TData, TScope>(
  scope: TScope,
  data: TData | undefined,
  scopesEqual: (left: TScope, right: TScope) => boolean,
): TData | undefined {
  const [retained, setRetained] = useState<RetainedQueryData<TData, TScope> | null>(null);
  if (data !== undefined && (retained?.data !== data || !scopesEqual(retained.scope, scope))) {
    setRetained({ data, scope });
    return data;
  }
  if (data !== undefined) {
    return data;
  }
  return retained !== null && scopesEqual(retained.scope, scope) ? retained.data : undefined;
}
