export function emptyArray<T>(value: T[] | null | undefined): T[] {
  return value ?? [];
}
