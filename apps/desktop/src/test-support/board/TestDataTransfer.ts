import { type Mock, vi } from "vitest";

export class TestDataTransfer {
  readonly #values = new Map<string, string>();
  effectAllowed = "all";
  dropEffect = "none";
  readonly setDragImage: Mock<(image: Element, x: number, y: number) => void> = vi.fn();

  get types(): readonly string[] {
    return [...this.#values.keys()];
  }

  setData(type: string, value: string): void {
    this.#values.set(type, value);
  }

  getData(type: string): string {
    return this.#values.get(type) ?? "";
  }
}
