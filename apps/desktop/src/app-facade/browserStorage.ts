export type BrowserStorageArea = "local" | "session";
export type BrowserStorageOperation = "read" | "write" | "remove";

export class BrowserStorageError extends Error {
  readonly area: BrowserStorageArea;
  readonly key: string;
  readonly operation: BrowserStorageOperation;

  constructor(area: BrowserStorageArea, operation: BrowserStorageOperation, key: string, cause: unknown) {
    super(`Browser ${area} storage ${operation} failed for key "${key}".`, { cause });
    this.name = "BrowserStorageError";
    this.area = area;
    this.key = key;
    this.operation = operation;
  }
}

export type BrowserStorageResult<T> =
  | Readonly<{
      ok: true;
      value: T;
    }>
  | Readonly<{
      ok: false;
      error: BrowserStorageError;
    }>;

export function readBrowserStorage(
  area: BrowserStorageArea,
  key: string,
): BrowserStorageResult<string | null> {
  return runStorageOperation(area, "read", key, (storage) => storage.getItem(key));
}

export function writeBrowserStorage(
  area: BrowserStorageArea,
  key: string,
  value: string,
): BrowserStorageResult<void> {
  return runStorageOperation(area, "write", key, (storage) => {
    storage.setItem(key, value);
  });
}

export function removeBrowserStorage(area: BrowserStorageArea, key: string): BrowserStorageResult<void> {
  return runStorageOperation(area, "remove", key, (storage) => {
    storage.removeItem(key);
  });
}

function runStorageOperation<T>(
  area: BrowserStorageArea,
  operation: BrowserStorageOperation,
  key: string,
  apply: (storage: Storage) => T,
): BrowserStorageResult<T> {
  try {
    const storage = area === "local" ? globalThis.localStorage : globalThis.sessionStorage;
    return {
      ok: true,
      value: apply(storage),
    };
  } catch (cause: unknown) {
    return {
      ok: false,
      error: new BrowserStorageError(area, operation, key, cause),
    };
  }
}
