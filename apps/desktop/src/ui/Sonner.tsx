import {
  CircleCheckIcon,
  InfoIcon,
  OctagonXIcon,
  TriangleAlertIcon,
} from "lucide-react";
import type { ComponentProps } from "react";
import { Toaster as SonnerToaster } from "sonner";

import { readEffectiveTheme } from "../appEnvironment";
import { Spinner } from "./Spinner";

type ToasterProps = ComponentProps<typeof SonnerToaster>;

export function Toaster(props: ToasterProps) {
  if (import.meta.env.MODE === "test") {
    return null;
  }

  return (
    <SonnerToaster
      closeButton
      icons={{
        success: <CircleCheckIcon className="size-4 text-[var(--color-success)]" />,
        info: <InfoIcon className="size-4 text-[var(--color-primary)]" />,
        warning: <TriangleAlertIcon className="size-4 text-[var(--color-warning)]" />,
        error: <OctagonXIcon className="size-4 text-[var(--color-error)]" />,
        loading: <Spinner size="sm" />,
      }}
      position="top-right"
      theme={readEffectiveTheme()}
      toastOptions={{
        classNames: {
          actionButton:
            "rounded-[var(--radius-m)] border border-transparent bg-transparent text-[var(--color-primary)]",
          closeButton:
            "border-[var(--color-outline)] bg-[var(--color-island-2)] text-[var(--color-on-island)]",
          content: "grid gap-[var(--space-1)]",
          default: "border-[var(--color-outline)]",
          description: "text-[var(--color-muted)]",
          error: "border-[var(--color-error)]",
          icon: "text-[var(--color-on-background)]",
          info: "border-[var(--color-primary)]",
          loading: "border-[var(--color-outline)]",
          success: "border-[var(--color-success)]",
          title: "font-extrabold text-[var(--color-on-island)]",
          toast:
            "rounded-[var(--radius-m)] border bg-[var(--color-island-2)] text-[var(--color-on-island)] shadow-[var(--shadow-island-1)] backdrop-blur-[20px]",
          warning: "border-[var(--color-warning)]",
        },
      }}
      {...props}
    />
  );
}
