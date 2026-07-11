export function cx(...values: readonly (string | false | null | undefined)[]): string {
  return values
    .filter((value) => value !== false && value !== null && value !== undefined && value.length > 0)
    .join(" ");
}
