export class SetupOperationID {
  readonly #value: string;

  private constructor(value: string) {
    this.#value = value;
  }

  static parse(value: string): SetupOperationID {
    if (!isSetupOperationID(value)) {
      throw new Error("Setup operation id must be a UUID v4.");
    }
    return new SetupOperationID(value);
  }

  toJSONValue(): string {
    return this.#value;
  }
}

const uuidSectionLengths = [8, 4, 4, 4, 12] as const;
const variantDigits = new Set(["8", "9", "a", "b"]);

function isSetupOperationID(value: string): boolean {
  const sections = value.split("-");
  return (
    sections.length === uuidSectionLengths.length &&
    sections.every((section, index) => isHexSection(section, uuidSectionLengths[index] ?? 0)) &&
    sections[2]?.charAt(0) === "4" &&
    variantDigits.has(sections[3]?.charAt(0).toLowerCase() ?? "")
  );
}

function isHexSection(section: string, expectedLength: number): boolean {
  if (section.length !== expectedLength) {
    return false;
  }
  for (const char of section) {
    if (!isHexDigit(char)) {
      return false;
    }
  }
  return true;
}

function isHexDigit(char: string): boolean {
  const code = char.charCodeAt(0);
  return (
    (code >= 48 && code <= 57) ||
    (code >= 65 && code <= 70) ||
    (code >= 97 && code <= 102)
  );
}

export function parseSetupOperationID(value: string): SetupOperationID {
  return SetupOperationID.parse(value);
}

export function newSetupOperationID(): SetupOperationID {
  return parseSetupOperationID(crypto.randomUUID());
}
