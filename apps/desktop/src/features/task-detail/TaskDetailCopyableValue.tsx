import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { errorMessage } from "@/api";
import { useAppServices } from "@/app-facade";
import { writeClipboardText } from "@/shared/native-clipboard";
import { CopyableValueButton, showStatusToast } from "@/ui";
import {
  taskDetailCopyValueNoticePolicy,
  type TaskDetailCopyValueKind,
  type TaskDetailCopyValueLocalization,
} from "./taskDetailCopyValuePolicy";

export function TaskDetailCopyableValue({
  children,
  className,
  clipboardValue,
  kind,
}: Readonly<{
  children: ReactNode;
  className?: string;
  clipboardValue: string;
  kind: TaskDetailCopyValueKind;
}>) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const policy = taskDetailCopyValueNoticePolicy(kind);
  return (
    <CopyableValueButton
      accessibleLabel={localize(policy.copyLabel, t)}
      onActivate={() => {
        void writeClipboardText(clipboardValue, nativeBridge)
          .then(() => {
            showStatusToast({
              id: policy.success.id,
              title: localize(policy.success, t),
              tone: "success",
            });
          })
          .catch((error: unknown) => {
            showStatusToast({
              body: errorMessage(error),
              id: policy.failure.id,
              title: localize(policy.failure, t),
              tone: "danger",
            });
          });
      }}
      {...(className === undefined ? {} : { className })}
    >
      {children}
    </CopyableValueButton>
  );
}

function localize(
  selection: TaskDetailCopyValueLocalization,
  t: ReturnType<typeof useTranslation>["t"],
): string {
  return "interpolation" in selection
    ? t(selection.titleKey, selection.interpolation)
    : t(selection.titleKey);
}
