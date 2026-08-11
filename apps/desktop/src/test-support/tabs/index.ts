import { within } from "@testing-library/react";

export function getTabs(container: HTMLElement): HTMLElement[] {
  return within(container).getAllByRole("tab");
}

export function getSelectedTabs(container: HTMLElement): HTMLElement[] {
  return getTabs(container).filter((tab) => tab.getAttribute("aria-selected") === "true");
}

export function getUnselectedTab(container: HTMLElement): HTMLElement {
  const tab = getTabs(container).find((candidate) => candidate.getAttribute("aria-selected") === "false");
  if (tab === undefined) throw new Error("Expected an unselected tab.");
  return tab;
}
