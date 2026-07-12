import {
  cloneElement,
  useCallback,
  useEffect,
  useRef,
  useState,
  type FocusEvent,
  type KeyboardEvent,
  type ReactElement,
} from "react";
import { useTranslation } from "react-i18next";

import { Popover, PopoverContent, PopoverTrigger } from "../../ui";
import type { CreatableWorkflowNodeKind } from "./workflowEditorGraphMutationTypes";
import { creatableWorkflowNodeKinds } from "./workflowGraphNodeKinds";

export type WorkflowNodeKindSelectionModality = "keyboard" | "pointer";
export type WorkflowNodeKindPickerTriggerPolicy = "activation" | "toolbar";
type WorkflowNodeKindPickerOpenModality = WorkflowNodeKindSelectionModality | "hover" | null;

export function WorkflowNodeKindPicker({
  disabled = false,
  onTriggerActivate,
  onSelect,
  trigger,
  triggerPolicy,
}: Readonly<{
  disabled?: boolean | undefined;
  onTriggerActivate?: (() => boolean) | undefined;
  onSelect: (kind: CreatableWorkflowNodeKind, modality: WorkflowNodeKindSelectionModality) => void;
  trigger: ReactElement;
  triggerPolicy: WorkflowNodeKindPickerTriggerPolicy;
}>) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [openModality, setOpenModality] = useState<WorkflowNodeKindPickerOpenModality>(null);
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pointerInsideTriggerRef = useRef(false);
  const pointerInsideContentRef = useRef(false);
  const focusInsideRef = useRef(false);
  const selectionModalityRef = useRef<WorkflowNodeKindSelectionModality | null>(null);
  const suppressReturnedTriggerFocusRef = useRef(false);
  const cancelClose = useCallback(() => {
    if (closeTimerRef.current !== null) {
      clearTimeout(closeTimerRef.current);
      closeTimerRef.current = null;
    }
  }, []);
  const close = useCallback(() => {
    cancelClose();
    focusInsideRef.current = false;
    pointerInsideContentRef.current = false;
    pointerInsideTriggerRef.current = false;
    setOpen(false);
  }, [cancelClose]);
  const openPicker = useCallback((modality: Exclude<WorkflowNodeKindPickerOpenModality, null>) => {
    if (!disabled) {
      cancelClose();
      setOpenModality(modality);
      setOpen(true);
    }
  }, [cancelClose, disabled]);
  const scheduleClose = useCallback(() => {
    cancelClose();
    closeTimerRef.current = setTimeout(() => {
      closeTimerRef.current = null;
      if (!pointerInsideTriggerRef.current && !pointerInsideContentRef.current && !focusInsideRef.current) {
        close();
      }
    }, 120);
  }, [cancelClose, close]);
  useEffect(() => cancelClose, [cancelClose]);

  const onTriggerClick = useCallback(
    (event: React.MouseEvent<HTMLElement>) => {
      event.preventDefault();
      event.stopPropagation();
      if (onTriggerActivate?.() === false) {
        return;
      }
      openPicker(event.detail === 0 ? "keyboard" : "pointer");
    },
    [onTriggerActivate, openPicker],
  );
  const onTriggerKeyDown = useCallback(
    (event: KeyboardEvent<HTMLElement>) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        event.stopPropagation();
        if (onTriggerActivate?.() === false) {
          return;
        }
        openPicker("keyboard");
      }
    },
    [onTriggerActivate, openPicker],
  );
  const onContentBlur = useCallback(
    (event: FocusEvent<HTMLElement>) => {
      if (event.relatedTarget instanceof Node && event.currentTarget.contains(event.relatedTarget)) {
        return;
      }
      focusInsideRef.current = false;
      if (triggerPolicy === "toolbar") {
        scheduleClose();
      }
    },
    [scheduleClose, triggerPolicy],
  );
  const onSelectChoice = useCallback(
    (kind: CreatableWorkflowNodeKind, event: React.MouseEvent<HTMLButtonElement>) => {
      const modality: WorkflowNodeKindSelectionModality = event.detail === 0 ? "keyboard" : "pointer";
      selectionModalityRef.current = modality;
      close();
      onSelect(kind, modality);
    },
    [close, onSelect],
  );
  const triggerProps =
    triggerPolicy === "toolbar"
      ? {
          onBlur: () => {
            suppressReturnedTriggerFocusRef.current = false;
            focusInsideRef.current = false;
            scheduleClose();
          },
          onClick: onTriggerClick,
          onFocus: () => {
            if (suppressReturnedTriggerFocusRef.current) {
              suppressReturnedTriggerFocusRef.current = false;
              return;
            }
            focusInsideRef.current = true;
            openPicker("keyboard");
          },
          onKeyDown: onTriggerKeyDown,
          onPointerEnter: () => {
            pointerInsideTriggerRef.current = true;
            openPicker("hover");
          },
          onPointerLeave: () => {
            pointerInsideTriggerRef.current = false;
            scheduleClose();
          },
        }
      : {
          onClick: onTriggerClick,
          onKeyDown: onTriggerKeyDown,
        };

  return (
    <Popover
      onOpenChange={(nextOpen) => {
        if (!nextOpen) {
          close();
        }
      }}
      open={open}
    >
      <PickerTrigger trigger={trigger} triggerProps={triggerProps} />
      <PopoverContent
        align="start"
        className="w-[220px] gap-[var(--space-1)] p-[var(--space-2)]"
        collisionPadding={12}
        level={3}
        onBlur={onContentBlur}
        onCloseAutoFocus={(event) => {
          if (selectionModalityRef.current !== null) {
            event.preventDefault();
            selectionModalityRef.current = null;
            return;
          }
          suppressReturnedTriggerFocusRef.current = true;
        }}
        onFocus={() => {
          focusInsideRef.current = true;
          cancelClose();
        }}
        onOpenAutoFocus={(event) => {
          if (openModality !== "keyboard") {
            event.preventDefault();
          }
        }}
        onPointerEnter={() => {
          pointerInsideContentRef.current = true;
          cancelClose();
        }}
        onPointerLeave={() => {
          pointerInsideContentRef.current = false;
          if (triggerPolicy === "toolbar") {
            scheduleClose();
          }
        }}
        side="right"
      >
        {creatableWorkflowNodeKinds.map((choice) => (
          <button
            className="rounded-[var(--radius-m)] border border-transparent bg-transparent px-[var(--space-3)] py-[var(--space-2)] text-left text-sm font-semibold text-[var(--color-on-island)] outline-none transition-colors hover:bg-[var(--color-island-2)] focus-visible:border-[var(--color-primary)]"
            key={choice.kind}
            onClick={(event) => {
              onSelectChoice(choice.kind, event);
            }}
            onKeyDown={(event) => {
              if (event.key !== "Enter" && event.key !== " ") {
                return;
              }
              event.preventDefault();
              selectionModalityRef.current = "keyboard";
              close();
              onSelect(choice.kind, "keyboard");
            }}
            type="button"
          >
            {t(choice.labelKey)}
          </button>
        ))}
      </PopoverContent>
    </Popover>
  );
}

function PickerTrigger({
  trigger,
  triggerProps,
}: Readonly<{ trigger: ReactElement; triggerProps: Record<string, unknown> }>) {
  return <PopoverTrigger asChild>{cloneElement(trigger, triggerProps)}</PopoverTrigger>;
}
