export type NativeNotificationBackendID = number;

const maxBackendNotificationID = 2_147_483_647;
const hashMultiplier = 16_777_619;
const hashSeed = 2_166_136_261;

export class NativeNotificationIDMapper {
  readonly #stringToBackendID = new Map<string, NativeNotificationBackendID>();
  readonly #backendIDToString = new Map<NativeNotificationBackendID, string>();

  resolveBackendID(id: string): NativeNotificationBackendID {
    if (id.length === 0) {
      throw new Error("Native notification id must be a non-empty string.");
    }
    const existing = this.#stringToBackendID.get(id);
    if (existing !== undefined) {
      return existing;
    }
    const backendID = this.#allocateBackendID(id);
    this.#stringToBackendID.set(id, backendID);
    this.#backendIDToString.set(backendID, id);
    return backendID;
  }

  resolveStringID(backendID: NativeNotificationBackendID): string | null {
    return this.#backendIDToString.get(backendID) ?? null;
  }

  #allocateBackendID(id: string): NativeNotificationBackendID {
    const initial = hashNativeNotificationIDUnchecked(id) - 1;
    for (let attempt = 0; attempt < maxBackendNotificationID; attempt += 1) {
      const candidate = ((initial + attempt) % maxBackendNotificationID) + 1;
      const existing = this.#backendIDToString.get(candidate);
      if (existing === undefined || existing === id) {
        return candidate;
      }
    }
    throw new Error("Native notification backend id space is exhausted.");
  }
}

function hashNativeNotificationIDUnchecked(id: string): NativeNotificationBackendID {
  let hash = hashSeed;
  for (const char of id) {
    hash ^= char.codePointAt(0) ?? 0;
    hash = Math.imul(hash, hashMultiplier) >>> 0;
  }
  return (hash % maxBackendNotificationID) + 1;
}
