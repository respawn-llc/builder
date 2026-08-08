import {
  boardFiltersEqual,
  canonicalBoardFilter,
  type BoardFilter,
  type BoardFilterInput,
  type BoardNodeCardsPage,
  type WorkflowBoard,
} from "@/api";

export type BoardFilterGeneration = Readonly<{
  generation: number;
  filter: BoardFilter;
  retiring: boolean;
}>;

export type BoardFilterGenerationSnapshot = Readonly<{
  active: BoardFilterGeneration;
  desiredFilter: BoardFilter | null;
}>;

export type BoardFilterGenerationController = Readonly<{
  admitBoardTransport(
    generation: number,
    requestIdentity: string,
    operation: () => Promise<WorkflowBoard>,
  ): BoardTransportAdmission<WorkflowBoard>;
  admitCardsTransport(
    generation: number,
    requestIdentity: string,
    operation: () => Promise<BoardNodeCardsPage>,
  ): BoardTransportAdmission<BoardNodeCardsPage>;
  getSnapshot(): BoardFilterGenerationSnapshot;
  registerOrchestration(generation: number, identity: string, orchestration: Promise<unknown>): boolean;
  registerCancellationBarrier(generation: number, barrier: Promise<unknown>): void;
  setDesiredFilter(filter: BoardFilterInput): void;
  subscribe(listener: () => void): () => void;
}>;

export type BoardTransportAdmission<T> =
  Readonly<{ kind: "admitted"; promise: Promise<T> }> | Readonly<{ kind: "denied" }>;

export function createBoardFilterGenerationController(
  initialFilter: BoardFilterInput,
  options: Readonly<{
    onPromoted?: ((generation: BoardFilterGeneration) => void) | undefined;
    onRetiring?: ((generation: BoardFilterGeneration) => Promise<void> | void) | undefined;
    onBackgroundError?: ((error: unknown) => void) | undefined;
  }> = {},
): BoardFilterGenerationController {
  return new ActiveLatestBoardFilterController(initialFilter, options);
}

class ActiveLatestBoardFilterController implements BoardFilterGenerationController {
  readonly #listeners = new Set<() => void>();
  readonly #boardLeases = new OperationLeaseRegistry<WorkflowBoard>();
  readonly #cardLeases = new OperationLeaseRegistry<BoardNodeCardsPage>();
  readonly #orchestrations = new Map<string, ReadonlySet<Promise<unknown>>>();
  readonly #barriers = new Set<Promise<unknown>>();
  readonly #onBackgroundError: ((error: unknown) => void) | undefined;
  readonly #onPromoted: ((generation: BoardFilterGeneration) => void) | undefined;
  readonly #onRetiring: ((generation: BoardFilterGeneration) => Promise<void> | void) | undefined;
  #snapshot: BoardFilterGenerationSnapshot;

  constructor(
    initialFilter: BoardFilterInput,
    options: Readonly<{
      onPromoted?: ((generation: BoardFilterGeneration) => void) | undefined;
      onRetiring?: ((generation: BoardFilterGeneration) => Promise<void> | void) | undefined;
      onBackgroundError?: ((error: unknown) => void) | undefined;
    }>,
  ) {
    this.#onBackgroundError = options.onBackgroundError;
    this.#onPromoted = options.onPromoted;
    this.#onRetiring = options.onRetiring;
    this.#snapshot = {
      active: {
        generation: 1,
        filter: canonicalBoardFilter(initialFilter),
        retiring: false,
      },
      desiredFilter: null,
    };
  }

  getSnapshot(): BoardFilterGenerationSnapshot {
    return this.#snapshot;
  }

  registerOrchestration(generation: number, identity: string, orchestration: Promise<unknown>): boolean {
    if (!this.#admissionOpen(generation)) {
      return false;
    }
    const generationIdentity = scopedIdentity(generation, identity);
    const current = this.#orchestrations.get(generationIdentity) ?? new Set();
    if (current.has(orchestration)) {
      return true;
    }
    this.#orchestrations.set(generationIdentity, new Set(current).add(orchestration));
    void orchestration
      .catch(() => {
        // TanStack owns query error presentation; the controller owns only settlement.
      })
      .finally(() => {
        const registered = this.#orchestrations.get(generationIdentity);
        if (registered?.has(orchestration) !== true) {
          return;
        }
        const remaining = new Set(registered);
        remaining.delete(orchestration);
        if (remaining.size === 0) {
          this.#orchestrations.delete(generationIdentity);
        } else {
          this.#orchestrations.set(generationIdentity, remaining);
        }
        this.#promoteWhenSettled();
      });
    return true;
  }

  registerCancellationBarrier(generation: number, barrier: Promise<unknown>): void {
    if (
      this.#snapshot.active.generation !== generation ||
      !this.#snapshot.active.retiring ||
      this.#barriers.has(barrier)
    ) {
      return;
    }
    this.#trackBarrier(barrier);
  }

  setDesiredFilter(filter: BoardFilterInput): void {
    const desiredFilter = canonicalBoardFilter(filter);
    const { active } = this.#snapshot;
    if (!active.retiring && boardFiltersEqual(active.filter, desiredFilter)) {
      return;
    }
    if (!this.#hasUnsettledWork() && !active.retiring) {
      this.#promote(desiredFilter);
      return;
    }
    this.#snapshot = {
      active: active.retiring ? active : { ...active, retiring: true },
      desiredFilter,
    };
    this.#emit();
    if (!active.retiring) {
      this.#beginRetirement();
    }
    this.#promoteWhenSettled();
  }

  admitBoardTransport(
    generation: number,
    requestIdentity: string,
    operation: () => Promise<WorkflowBoard>,
  ): BoardTransportAdmission<WorkflowBoard> {
    return this.#admitTransport(this.#boardLeases, generation, requestIdentity, operation);
  }

  admitCardsTransport(
    generation: number,
    requestIdentity: string,
    operation: () => Promise<BoardNodeCardsPage>,
  ): BoardTransportAdmission<BoardNodeCardsPage> {
    return this.#admitTransport(this.#cardLeases, generation, requestIdentity, operation);
  }

  #admitTransport<T>(
    registry: OperationLeaseRegistry<T>,
    generation: number,
    requestIdentity: string,
    operation: () => Promise<T>,
  ): BoardTransportAdmission<T> {
    if (!this.#admissionOpen(generation)) {
      return { kind: "denied" };
    }
    const generationIdentity = scopedIdentity(generation, requestIdentity);
    const existing = registry.leases.get(generationIdentity);
    if (existing !== undefined) {
      return { kind: "admitted", promise: existing };
    }
    const promise = operation();
    registry.install(generationIdentity, promise, () => {
      this.#promoteWhenSettled();
    });
    return { kind: "admitted", promise };
  }

  subscribe(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => {
      this.#listeners.delete(listener);
    };
  }

  #beginRetirement(): void {
    if (this.#onRetiring === undefined) {
      return;
    }
    let barrier: Promise<void>;
    try {
      barrier = Promise.resolve(this.#onRetiring(this.#snapshot.active));
    } catch (error) {
      barrier = Promise.reject(
        error instanceof Error ? error : new Error("Board generation retirement failed.", { cause: error }),
      );
    }
    this.#trackBarrier(barrier);
  }

  #trackBarrier(barrier: Promise<unknown>): void {
    this.#barriers.add(barrier);
    void barrier
      .catch((error: unknown) => {
        this.#onBackgroundError?.(error);
      })
      .finally(() => {
        this.#barriers.delete(barrier);
        this.#promoteWhenSettled();
      });
  }

  #admissionOpen(generation: number): boolean {
    return this.#snapshot.active.generation === generation && !this.#snapshot.active.retiring;
  }

  #hasUnsettledWork(): boolean {
    return (
      this.#boardLeases.size > 0 ||
      this.#cardLeases.size > 0 ||
      this.#orchestrations.size > 0 ||
      this.#barriers.size > 0
    );
  }

  #promoteWhenSettled(): void {
    const desiredFilter = this.#snapshot.desiredFilter;
    if (!this.#snapshot.active.retiring || desiredFilter === null || this.#hasUnsettledWork()) {
      return;
    }
    this.#promote(desiredFilter);
  }

  #promote(filter: BoardFilter): void {
    const active: BoardFilterGeneration = {
      generation: this.#snapshot.active.generation + 1,
      filter,
      retiring: false,
    };
    this.#snapshot = {
      active,
      desiredFilter: null,
    };
    this.#emit();
    this.#onPromoted?.(active);
  }

  #emit(): void {
    for (const listener of this.#listeners) {
      listener();
    }
  }
}

class OperationLeaseRegistry<T> {
  readonly leases = new Map<string, Promise<T>>();

  get size(): number {
    return this.leases.size;
  }

  install(identity: string, promise: Promise<T>, onSettled: () => void): void {
    this.leases.set(identity, promise);
    const settle = (): void => {
      if (this.leases.get(identity) !== promise) {
        return;
      }
      this.leases.delete(identity);
      onSettled();
    };
    void promise.then(settle, settle);
  }
}

function scopedIdentity(generation: number, identity: string): string {
  return `${generation.toString()}:${identity}`;
}
