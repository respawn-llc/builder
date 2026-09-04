export function firstPresent(...values: readonly (string | null | undefined)[]): string | undefined {
  return (
    values.find(
      (value): value is string => value !== undefined && value !== null && value.trim().length > 0,
    ) ?? undefined
  );
}
