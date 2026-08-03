import { useEffect, useState } from "react";

export function prefersReducedMotion(): boolean {
  return (
    window.matchMedia instanceof Function && window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

export function useReducedMotion(): boolean {
  const [reducedMotion, setReducedMotion] = useState(
    () => prefersReducedMotion(),
  );
  useEffect(() => {
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
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
