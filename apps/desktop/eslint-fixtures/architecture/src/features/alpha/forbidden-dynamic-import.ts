export async function forbiddenDynamicImport(): Promise<unknown> {
  return import("@/features/beta");
}
