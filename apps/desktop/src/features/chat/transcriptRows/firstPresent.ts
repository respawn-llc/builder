export function firstPresent(...values: readonly (string | null | undefined)[]): string {
  for (const value of values) {
    if (value !== undefined && value !== null && value.trim().length > 0) {
      return value;
    }
  }
  return "";
}
