import { useEffect, useState } from "react";

export function useTaskSearchShortcuts(onOpen: () => void): void {
  useEffect(() => {
    const handleKeyDown = (event: globalThis.KeyboardEvent): void => {
      if (!isTaskSearchShortcut(event)) {
        return;
      }
      event.preventDefault();
      if (!event.repeat) {
        onOpen();
      }
    };
    window.addEventListener("keydown", handleKeyDown);
    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [onOpen]);
}

function isTaskSearchShortcut(event: globalThis.KeyboardEvent): boolean {
  const saveShortcut =
    (event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.code === "KeyS";
  const altSpaceShortcut =
    event.altKey && !event.metaKey && !event.ctrlKey && !event.shiftKey && event.code === "Space";
  return saveShortcut || altSpaceShortcut;
}

export function useDebouncedText(value: string, delayMs: number): string {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      setDebounced(value);
    }, delayMs);
    return () => {
      window.clearTimeout(timer);
    };
  }, [delayMs, value]);
  return debounced;
}
