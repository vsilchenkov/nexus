import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

// cn — объединяет классы (clsx) и разрешает конфликты Tailwind (twMerge).
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
