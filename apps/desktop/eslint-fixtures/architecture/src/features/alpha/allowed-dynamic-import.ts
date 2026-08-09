export async function allowedDynamicImport(): Promise<unknown> {
  return import("@/api");
}
