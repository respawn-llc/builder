import { useEffect, useState, type RefObject } from "react";

export function useColumnVisibility(
  scrollportRef: RefObject<HTMLElement | null>,
  columnElement: HTMLElement | null,
): boolean {
  const [isVisible, setIsVisible] = useState(false);
  useEffect(() => {
    if (columnElement === null || typeof IntersectionObserver === "undefined") {
      return;
    }
    const observer = new IntersectionObserver(
      (entries) => {
        setIsVisible(entries.some((entry) => entry.isIntersecting));
      },
      { root: scrollportRef.current, rootMargin: "480px 640px" },
    );
    observer.observe(columnElement);
    return () => {
      observer.disconnect();
    };
  }, [columnElement, scrollportRef]);
  if (typeof IntersectionObserver === "undefined") {
    return columnElement !== null;
  }
  return columnElement !== null && isVisible;
}
