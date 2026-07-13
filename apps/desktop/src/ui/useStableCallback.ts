import { useCallback, useLayoutEffect, useRef } from "react";

export function useStableCallback<TArguments extends unknown[], TResult>(
  callback: (...args: TArguments) => TResult,
): (...args: TArguments) => TResult {
  const callbackRef = useRef(callback);
  useLayoutEffect(() => {
    callbackRef.current = callback;
  }, [callback]);
  return useCallback((...args: TArguments) => callbackRef.current(...args), []);
}
