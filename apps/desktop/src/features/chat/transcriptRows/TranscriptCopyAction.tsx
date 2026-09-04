import { Check, Copy } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { errorMessage } from "@/api";
import { useAppServices } from "@/app-facade";
import { writeClipboardText } from "@/shared/native-clipboard";
import { cx, IconTooltipButton, showStatusToast } from "@/ui";

import "./transcriptRows.css";

export type TranscriptCopyActionProps = Readonly<{
  value: string;
  copyLabel: string;
  copiedLabel: string;
  failureLabel: string;
}>;

export function TranscriptCopyAction({
  copiedLabel,
  copyLabel,
  failureLabel,
  value,
}: TranscriptCopyActionProps) {
  const { nativeBridge } = useAppServices();
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current);
    },
    [],
  );

  return (
    <IconTooltipButton
      label={copied ? copiedLabel : copyLabel}
      onClick={() => {
        void writeClipboardText(value, nativeBridge)
          .then(() => {
            setCopied(true);
            if (timer.current !== null) clearTimeout(timer.current);
            timer.current = setTimeout(() => {
              timer.current = null;
              setCopied(false);
            }, 2_000);
          })
          .catch((error: unknown) => {
            showStatusToast({
              body: errorMessage(error),
              id: "chat-transcript-copy-failed",
              title: failureLabel,
              tone: "danger",
            });
          });
      }}
      size="icon-sm"
    >
      <span aria-hidden="true" className="chat-transcript-copy-icon relative grid size-4 place-items-center">
        <Copy className={cx("absolute size-4 transition-opacity", copied && "opacity-0")} />
        <Check
          className={cx(
            "absolute size-4 text-[var(--color-success)] transition-opacity",
            copied ? "opacity-100" : "opacity-0",
          )}
        />
      </span>
    </IconTooltipButton>
  );
}
