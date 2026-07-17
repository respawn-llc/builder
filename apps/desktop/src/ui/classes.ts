import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}

export function cx(...values: readonly (string | false | null | undefined)[]): string {
  return values
    .filter((value) => value !== false && value !== null && value !== undefined && value.length > 0)
    .join(" ");
}
