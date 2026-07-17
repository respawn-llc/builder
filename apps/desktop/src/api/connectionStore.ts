export type ConnectionPhase = "idle" | "connecting" | "connected" | "disconnected";

export type ConnectionSnapshot = Readonly<{
  phase: ConnectionPhase;
  lastError: string | null;
  generation: number;
}>;

export type ConnectionListener = () => void;

export class ConnectionStore {
  #snapshot: ConnectionSnapshot = { phase: "idle", lastError: null, generation: 0 };
  #listeners = new Set<ConnectionListener>();

  snapshot(): ConnectionSnapshot {
    return this.#snapshot;
  }

  subscribe(listener: ConnectionListener): () => void {
    this.#listeners.add(listener);
    return () => {
      this.#listeners.delete(listener);
    };
  }

  set(phase: ConnectionPhase, lastError: string | null = null): void {
    if (this.#snapshot.phase === phase && this.#snapshot.lastError === lastError) {
      return;
    }
    this.#snapshot = { phase, lastError, generation: this.#snapshot.generation + 1 };
    for (const listener of this.#listeners) {
      listener();
    }
  }
}
