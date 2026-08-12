import { useLayoutEffect, useState, type RefObject } from "react";

export function useElementHeight(ref: RefObject<HTMLElement | null>): number {
  const [height, setHeight] = useState(0);
  useLayoutEffect(() => {
    const element = ref.current;
    if (element === null) return;
    const measure = () => {
      const nextHeight = element.getBoundingClientRect().height;
      setHeight((currentHeight) => (currentHeight === nextHeight ? currentHeight : nextHeight));
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => {
      observer.disconnect();
    };
  }, [ref]);
  return height;
}
