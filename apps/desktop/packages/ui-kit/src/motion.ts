import { useEffect, useState } from "react";

const reducedMotionQuery = "(prefers-reduced-motion: reduce)";

export function useReducedMotion(): boolean {
  const [reducedMotion, setReducedMotion] = useState(
    () => window.matchMedia instanceof Function && window.matchMedia(reducedMotionQuery).matches,
  );
  useEffect(() => {
    if (!(window.matchMedia instanceof Function)) {
      return undefined;
    }
    const media = window.matchMedia(reducedMotionQuery);
    const handleChange = () => {
      setReducedMotion(media.matches);
    };
    media.addEventListener("change", handleChange);
    return () => {
      media.removeEventListener("change", handleChange);
    };
  }, []);
  return reducedMotion;
}
