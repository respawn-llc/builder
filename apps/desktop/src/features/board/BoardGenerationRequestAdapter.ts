import { CancelledError, type QueryClient, type QueryKey } from "@tanstack/react-query";

import type { BoardNodeCardsPage, WorkflowBoard } from "@/api";
import type {
  BoardFilterGenerationController,
  BoardTransportAdmission,
} from "./BoardFilterGenerationController";
import type { BoardGenerationQueryRegistry } from "./BoardGenerationQueryRegistry";

type BoardGenerationRequest<T> = Readonly<{
  generation: number;
  queryKey: QueryKey;
  requestIdentity: string;
  signal: AbortSignal;
  transport: () => Promise<T>;
}>;

export type BoardGenerationRequestAdapter = Readonly<{
  requestBoard(input: BoardGenerationRequest<WorkflowBoard>): Promise<WorkflowBoard>;
  requestCards(input: BoardGenerationRequest<BoardNodeCardsPage>): Promise<BoardNodeCardsPage>;
}>;

export function createBoardGenerationRequestAdapter({
  controller,
  queryClient,
  queryRegistry,
}: Readonly<{
  controller: BoardFilterGenerationController;
  queryClient: QueryClient;
  queryRegistry: BoardGenerationQueryRegistry;
}>): BoardGenerationRequestAdapter {
  async function request<T>(
    input: BoardGenerationRequest<T>,
    admit: (
      generation: number,
      requestIdentity: string,
      transport: () => Promise<T>,
    ) => BoardTransportAdmission<T>,
  ): Promise<T> {
    const { generation, queryKey, requestIdentity, signal, transport } = input;
    const query = queryClient.getQueryCache().find({ queryKey, exact: true });
    const orchestration = query?.promise;
    if (query === undefined || orchestration === undefined) {
      throw new Error(
        `Board generation ${generation.toString()} request ${requestIdentity} has no active TanStack orchestration.`,
      );
    }
    queryRegistry.register(generation, queryKey);
    if (!controller.registerOrchestration(generation, query.queryHash, orchestration)) {
      denyQuery(generation, queryKey);
    }
    const admission = admit(generation, requestIdentity, transport);
    if (admission.kind === "denied") {
      denyQuery(generation, queryKey);
    }
    try {
      const result = await admission.promise;
      if (signal.aborted) {
        throw silentCancellation();
      }
      return result;
    } catch (error) {
      if (signal.aborted) {
        throw silentCancellation();
      }
      throw error;
    }
  }

  function denyQuery(deniedGeneration: number, deniedQueryKey: QueryKey): never {
    const barrier = queryClient.cancelQueries(
      { queryKey: deniedQueryKey, exact: true },
      { revert: true, silent: true },
    );
    controller.registerCancellationBarrier(deniedGeneration, barrier);
    throw silentCancellation();
  }

  return {
    async requestBoard(input) {
      return request(input, (generation, requestIdentity, transport) =>
        controller.admitBoardTransport(generation, requestIdentity, transport),
      );
    },
    async requestCards(input) {
      return request(input, (generation, requestIdentity, transport) =>
        controller.admitCardsTransport(generation, requestIdentity, transport),
      );
    },
  };
}

function silentCancellation(): CancelledError {
  return new CancelledError({ revert: true, silent: true });
}
