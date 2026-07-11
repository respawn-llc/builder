import { useCallback, useId, useLayoutEffect, useRef, useState } from "react";
import {
  closestCenter,
  DndContext,
  KeyboardSensor,
  PointerSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
} from "@dnd-kit/core";
import {
  SortableContext,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from "@dnd-kit/sortable";
import { GripVertical, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";
import { z } from "zod";

import type { WorkflowDefinition, WorkflowParameter } from "../../api";
import {
  Button,
  identifierInputAttributes,
  IslandSurface,
  SelectField,
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "../../ui";
import { cx } from "../../ui/classes";
import { fieldInputClassName } from "../../ui/fieldInputStyles";
import { DetailSection } from "./WorkflowInspectorPrimitives";
import { type DraftWorkflowEdge, type DraftWorkflowNode } from "./workflowEditorDraft";
import { type WorkflowEditorDraftController } from "./workflowEditorDraftBridgeCore";
import {
  transitionKeyedParameterPlaceholderExample,
  workflowPromptTemplatePlaceholders,
  type PromptTemplatePlaceholder,
} from "./workflowPromptTemplatePlaceholders";
import { derivedNodeWiring, joinProviderOptions } from "./workflowInspectorWiring";

const sortableRowIDSchema = z.string();

export function PromptTemplateEditor({
  onPromptChange,
  parameters,
  promptTemplate,
}: Readonly<{
  onPromptChange: (promptTemplate: string) => void;
  parameters: readonly Pick<WorkflowParameter, "key">[];
  promptTemplate: string;
}>) {
  const { t } = useTranslation();
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const pendingSelectionRef = useRef<Readonly<{ cursor: number; scrollToEnd: boolean }> | null>(null);
  const promptInputId = useId();
  useLayoutEffect(() => {
    const pending = pendingSelectionRef.current;
    const textarea = textareaRef.current;
    if (pending === null || textarea === null) {
      return;
    }
    pendingSelectionRef.current = null;
    textarea.focus({ preventScroll: true });
    textarea.setSelectionRange(pending.cursor, pending.cursor);
    if (pending.scrollToEnd) {
      textarea.scrollTop = textarea.scrollHeight;
    }
  }, [promptTemplate]);
  const insertPlaceholder = useCallback(
    (placeholder: string) => {
      const textarea = textareaRef.current;
      const currentValue = textarea?.value ?? promptTemplate;
      const hasCursor = textarea !== null && document.activeElement === textarea;
      const insertAt = hasCursor ? textarea.selectionEnd : currentValue.length;
      const nextValue = `${currentValue.slice(0, insertAt)}${placeholder}${currentValue.slice(insertAt)}`;
      const nextCursor = insertAt + placeholder.length;
      pendingSelectionRef.current = { cursor: nextCursor, scrollToEnd: !hasCursor };
      onPromptChange(nextValue);
    },
    [onPromptChange, promptTemplate],
  );
  return (
    <DetailSection title={t("workflowEditor.prompt")} titleHelp={t("workflowEditor.promptHelp")}>
      <label className="sr-only" htmlFor={promptInputId}>
        {t("workflowEditor.prompt")}
      </label>
      <div className="grid gap-[var(--space-2)]">
        <textarea
          className={cx(fieldInputClassName, "min-h-24")}
          id={promptInputId}
          onChange={(event) => {
            onPromptChange(event.target.value);
          }}
          ref={textareaRef}
          value={promptTemplate}
        />
        <PromptPlaceholderChips onInsert={insertPlaceholder} parameters={parameters} />
      </div>
    </DetailSection>
  );
}

function PromptPlaceholderChips({
  onInsert,
  parameters,
}: Readonly<{
  onInsert: (placeholder: string) => void;
  parameters: readonly Pick<WorkflowParameter, "key">[];
}>) {
  const { t } = useTranslation();
  const placeholders = workflowPromptTemplatePlaceholders(parameters);
  return (
    <div
      aria-label={t("workflowEditor.promptPlaceholders")}
      className="flex flex-wrap gap-[var(--space-1)]"
      role="group"
    >
      <TooltipProvider delayDuration={0}>
        {placeholders.map((placeholder) => (
          <PromptPlaceholderChip
            key={placeholder.label}
            onInsert={onInsert}
            placeholder={placeholder}
          />
        ))}
      </TooltipProvider>
    </div>
  );
}

function PromptPlaceholderChip({
  onInsert,
  placeholder,
}: Readonly<{
  onInsert: (placeholder: string) => void;
  placeholder: PromptTemplatePlaceholder;
}>) {
  const { t } = useTranslation();
  const [infoOpen, setInfoOpen] = useState(false);
  const className = cx(promptPlaceholderChipBaseClassName, promptPlaceholderChipToneClassNames[placeholder.tone]);
  if (placeholder.kind === "insert") {
    return (
      <button
        className={className}
        data-placeholder-tone={placeholder.tone}
        onClick={() => {
          onInsert(placeholder.value);
        }}
        onPointerDown={(event) => {
          event.preventDefault();
        }}
        type="button"
      >
        {placeholder.label}
      </button>
    );
  }
  return (
    <Tooltip onOpenChange={setInfoOpen} open={infoOpen}>
      <TooltipTrigger asChild>
        <button
          aria-label={placeholder.label}
          className={className}
          data-placeholder-tone={placeholder.tone}
          onBlur={() => {
            setInfoOpen(false);
          }}
          onClick={() => {
            setInfoOpen(true);
          }}
          onFocus={() => {
            setInfoOpen(true);
          }}
          onPointerDown={(event) => {
            event.preventDefault();
          }}
          onPointerEnter={() => {
            setInfoOpen(true);
          }}
          onPointerLeave={() => {
            setInfoOpen(false);
          }}
          type="button"
        >
          {placeholder.label}
        </button>
      </TooltipTrigger>
      <TooltipContent
        className="grid max-w-[24rem] gap-[var(--space-1)] whitespace-normal text-left"
        data-testid="transition-keyed-parameter-placeholder-help"
        level={3}
        side="top"
        sideOffset={6}
      >
        <span>
          {t("workflowEditor.promptTransitionScopedParameterHelpPrefix")}{" "}
          <code>{transitionKeyedParameterPlaceholderExample}</code>.
        </span>
        <span>{t("workflowEditor.promptTransitionScopedParameterHelpSuffix")}</span>
      </TooltipContent>
    </Tooltip>
  );
}

const promptPlaceholderChipBaseClassName =
  "rounded-full border px-[var(--space-1)] py-px text-[11px] font-semibold leading-4 transition-colors focus-visible:outline-none focus-visible:ring-[2px]";

const promptPlaceholderChipToneClassNames = {
  muted:
    "border-[var(--color-outline)] bg-[color-mix(in_srgb,var(--color-on-background)_5%,transparent)] text-[var(--color-muted)] hover:bg-[color-mix(in_srgb,var(--color-on-background)_8%,transparent)] focus-visible:ring-[color-mix(in_srgb,var(--color-muted)_35%,transparent)]",
  primary:
    "border-[color-mix(in_srgb,var(--color-primary)_45%,transparent)] bg-[color-mix(in_srgb,var(--color-primary)_10%,transparent)] text-[var(--color-primary)] hover:bg-[color-mix(in_srgb,var(--color-primary)_16%,transparent)] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_35%,transparent)]",
} satisfies Record<PromptTemplatePlaceholder["tone"], string>;

export function EditableEdgeParameters({
  controller,
  edge,
}: Readonly<{
  controller: WorkflowEditorDraftController;
  edge: DraftWorkflowEdge;
}>) {
  const { t } = useTranslation();
  const parameters = edge.parameters.map((parameter, index) => ({
    ...parameter,
    rowID: parameter.rowID ?? [edge.id, "parameter", "fallback", index.toString()].join(":"),
  }));
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  );
  return (
    <DetailSection title={t("workflowEditor.parameters")} titleHelp={t("workflowEditor.parametersHelp")}>
      <div className="grid gap-[var(--space-3)]">
        <Button
          onClick={() => {
            controller.dispatch({ edgeID: edge.id, type: "addEdgeParameter" });
          }}
          variant="secondary"
        >
          {t("workflowEditor.addParameter")}
        </Button>
        {edge.parameters.length === 0 ? (
          <p className="m-0 text-sm text-[var(--color-muted)]">{t("workflowEditor.none")}</p>
        ) : null}
        <DndContext
          collisionDetection={closestCenter}
          onDragEnd={(event) => {
            reorderEdgeParameter(controller, edge.id, event);
          }}
          sensors={sensors}
        >
          <SortableContext
            items={parameters.map((parameter) => parameter.rowID)}
            strategy={verticalListSortingStrategy}
          >
            <div className="grid gap-[var(--space-3)]">
              {parameters.map((parameter) => (
                <SortableEdgeParameter
                  controller={controller}
                  edgeID={edge.id}
                  key={parameter.rowID}
                  parameter={parameter}
                />
              ))}
            </div>
          </SortableContext>
        </DndContext>
      </div>
    </DetailSection>
  );
}

function SortableEdgeParameter({
  controller,
  edgeID,
  parameter,
}: Readonly<{
  controller: WorkflowEditorDraftController;
  edgeID: string;
  parameter: WorkflowParameter & Readonly<{ rowID: string }>;
}>) {
  const { t } = useTranslation();
  const keyID = useId();
  const descriptionID = useId();
  const { attributes, listeners, setActivatorNodeRef, setNodeRef, transform, transition } = useSortable({
    id: parameter.rowID,
  });
  const style = {
    transform:
      transform === null
        ? undefined
        : `translate3d(${transform.x.toString()}px, ${transform.y.toString()}px, 0)`,
    transition,
  };
  return (
    <IslandSurface
      as="div"
      className="workflow-editor-parameter relative grid gap-[var(--space-2)] rounded-[var(--radius-m)] p-[var(--space-3)]"
      data-parameter-key={parameter.key}
      data-testid="workflow-parameter"
      level={1}
      ref={setNodeRef}
      style={style}
    >
      <div
        aria-label={t("workflowEditor.reorderParameter")}
        className="absolute inset-0 cursor-grab rounded-[inherit] outline-none focus-visible:ring-[3px] focus-visible:ring-[color-mix(in_srgb,var(--color-primary)_35%,transparent)] active:cursor-grabbing"
        ref={setActivatorNodeRef}
        {...attributes}
        {...listeners}
      />
      <div className="pointer-events-none relative grid gap-[var(--space-2)]">
        <div className="flex min-w-0 items-center gap-[var(--space-2)]">
          <GripVertical
            aria-hidden="true"
            className="shrink-0 text-[var(--color-muted)]"
            size={18}
            strokeWidth={1.8}
          />
          <div className="pointer-events-auto min-w-0 flex-1">
            <label className="sr-only" htmlFor={keyID}>
              {t("workflowEditor.parameterKey")}
            </label>
            <input
              {...identifierInputAttributes}
              autoFocus={parameter.key.length === 0}
              className="app-region-no-drag min-w-0 w-full rounded-[var(--radius-m)] border border-[var(--color-outline)] bg-[var(--color-island-1)] px-[var(--space-2)] py-[var(--space-1)] font-bold text-[var(--color-on-island)] outline-none focus:border-[var(--color-primary)]"
              id={keyID}
              onChange={(event) => {
                controller.dispatch({
                  edgeID,
                  parameterRowID: parameter.rowID,
                  patch: { key: event.target.value.replaceAll("\n", " ") },
                  type: "updateEdgeParameter",
                });
              }}
              placeholder={t("workflowEditor.parameterKey")}
              type="text"
              value={parameter.key}
            />
          </div>
          <Button
            aria-label={t("workflowEditor.deleteParameter")}
            className="pointer-events-auto grid h-8 w-8 shrink-0 place-items-center rounded-full !border-transparent !bg-transparent !p-0"
            onClick={() => {
              controller.dispatch({ edgeID, parameterRowID: parameter.rowID, type: "deleteEdgeParameter" });
            }}
            variant="danger"
          >
            <Trash2 aria-hidden="true" size={17} strokeWidth={1.9} />
          </Button>
        </div>
        <div className="pointer-events-auto">
          <label className="sr-only" htmlFor={descriptionID}>
            {t("workflowEditor.parameterDescription")}
          </label>
          <input
            className={cx(fieldInputClassName, "px-[var(--space-2)] py-[var(--space-2)]")}
            id={descriptionID}
            onChange={(event) => {
              controller.dispatch({
                edgeID,
                parameterRowID: parameter.rowID,
                patch: { description: event.target.value },
                type: "updateEdgeParameter",
              });
            }}
            placeholder={t("workflowEditor.parameterDescription")}
            value={parameter.description}
          />
        </div>
      </div>
    </IslandSurface>
  );
}

function reorderEdgeParameter(
  controller: WorkflowEditorDraftController,
  edgeID: string,
  event: DragEndEvent,
): void {
  const overID = event.over?.id;
  if (overID === undefined || event.active.id === overID) {
    return;
  }
  const activeRowID = sortableRowIDSchema.safeParse(event.active.id);
  const overRowID = sortableRowIDSchema.safeParse(overID);
  if (!activeRowID.success || !overRowID.success) {
    return;
  }
  controller.dispatch({
    activeRowID: activeRowID.data,
    edgeID,
    overRowID: overRowID.data,
    type: "reorderEdgeParameter",
  });
}

export function EditableJoinProviders({
  controller,
  definition,
  node,
}: Readonly<{
  controller: WorkflowEditorDraftController;
  definition: WorkflowDefinition;
  node: DraftWorkflowNode;
}>) {
  const { t } = useTranslation();
  const requiredFields = derivedNodeWiring(definition, node.id).joinOutputFields;
  const providerByInput = new Map(node.joinInputProviders.map((provider) => [provider.inputName, provider]));
  return (
    <DetailSection title={t("workflowEditor.joinProviders")}>
      {requiredFields.length === 0 ? (
        <p className="m-0 text-sm text-[var(--color-muted)]">{t("workflowEditor.none")}</p>
      ) : (
        <div className="grid gap-[var(--space-3)]">
          {requiredFields.map((field) => {
            const selectedEdgeID = providerByInput.get(field.name)?.providerEdgeID ?? "";
            return (
              <SelectField
                hint={field.description}
                key={field.name}
                label={field.name}
                onValueChange={(value) => {
                  controller.dispatch({
                    inputName: field.name,
                    nodeID: node.id,
                    providerEdgeID: value,
                    type: "assignJoinInputProvider",
                  });
                }}
                options={joinProviderOptions(definition, node.id, selectedEdgeID)}
                placeholder={t("workflowEditor.selectProvider")}
                value={selectedEdgeID}
              />
            );
          })}
        </div>
      )}
    </DetailSection>
  );
}
