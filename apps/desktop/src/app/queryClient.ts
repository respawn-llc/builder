import { QueryClient } from "@tanstack/react-query";

export function createAppQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        staleTime: 4_000,
      },
      mutations: {
        retry: false,
      },
    },
  });
}
