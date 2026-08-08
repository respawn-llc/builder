import { hashKey, type QueryClient, type QueryKey } from "@tanstack/react-query";

export type BoardGenerationQueryRegistry = Readonly<{
  cancelGeneration(generation: number): Promise<void>;
  invalidateGeneration(generation: number): Promise<void>;
  register(generation: number, queryKey: QueryKey): void;
  releaseGeneration(generation: number): void;
}>;

export function createBoardGenerationQueryRegistry(queryClient: QueryClient): BoardGenerationQueryRegistry {
  const queryKeysByGeneration = new Map<number, Map<string, QueryKey>>();
  return {
    async cancelGeneration(generation) {
      await Promise.all(
        registeredQueryKeys(generation).map(async (queryKey) =>
          queryClient.cancelQueries({ queryKey, exact: true }, { revert: true, silent: true }),
        ),
      );
    },
    async invalidateGeneration(generation) {
      await Promise.all(
        registeredQueryKeys(generation).map(async (queryKey) =>
          queryClient.invalidateQueries({
            queryKey,
            exact: true,
            refetchType: "active",
          }),
        ),
      );
    },
    register(generation, queryKey) {
      const current = queryKeysByGeneration.get(generation) ?? new Map<string, QueryKey>();
      current.set(hashKey(queryKey), queryKey);
      queryKeysByGeneration.set(generation, current);
    },
    releaseGeneration(generation) {
      queryKeysByGeneration.delete(generation);
    },
  };

  function registeredQueryKeys(generation: number): readonly QueryKey[] {
    return [...(queryKeysByGeneration.get(generation)?.values() ?? [])];
  }
}
