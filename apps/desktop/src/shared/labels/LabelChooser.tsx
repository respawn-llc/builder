import type { TFunction } from "i18next";
import {
  useId,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type KeyboardEvent,
  type ReactElement,
  type SetStateAction,
} from "react";
import { useTranslation } from "react-i18next";
import { PlusIcon, SearchIcon } from "lucide-react";

import { decodeWorkflowLabelError, errorMessage, workflowLabelMaxIDs, type ProjectLabel } from "@/api";
import { isTextFieldSubmitShortcut, useAppServices, useTextFieldSubmitShortcut } from "@/app-facade";
import {
  Button,
  IconTooltipButton,
  Popover,
  PopoverContent,
  PopoverTrigger,
  SegmentedControl,
  Spinner,
  fieldInputClassName,
} from "@/ui";
import {
  LabelRenameEditor,
  LabelResultRow,
  UnlabeledResultRow,
  type DeleteState,
  type LabelFilterCondition,
  type LabelResultRowSelection,
  type RenameState,
} from "./LabelChooserRows";
import { labelNameContains, labelNamesEqual } from "./labelComparison";
import type { LabelFilterAction, LabelFilterState } from "./labelFilterState";
import { useProjectLabelCatalog, useProjectLabelCatalogMutations } from "./projectLabelHooks";

export type LabelChooserInvocation =
  | Readonly<{
      kind: "filter";
      state: LabelFilterState;
      onAction(action: LabelFilterAction): void;
    }>
  | Readonly<{
      kind: "assignment";
      selectedLabelIDs: readonly string[];
      onSelectionChange(labelID: string, selected: boolean): void;
    }>;

export type LabelChooserProps = Readonly<{
  invocation: LabelChooserInvocation;
  trigger: ReactElement;
}>;

type LabelChooserChoice = Readonly<{ kind: "unlabeled" }> | Readonly<{ kind: "label"; label: ProjectLabel }>;

export function LabelChooser({ invocation, trigger }: LabelChooserProps) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const catalog = useProjectLabelCatalog();
  const mutations = useProjectLabelCatalogMutations();
  const [search, setSearch] = useState("");
  const [keyboardHighlightedIndex, setKeyboardHighlightedIndex] = useState<number | null>(null);
  const [open, setOpen] = useState(false);
  const [rename, setRename] = useState<RenameState | null>(null);
  const [deletion, setDeletion] = useState<DeleteState | null>(null);
  const outsideInteractionRef = useRef(false);
  const searchErrorID = useId();
  const mutationErrorMessage = (error: unknown): string => {
    const labelError = decodeWorkflowLabelError(error);
    if (labelError === null) {
      return errorMessage(error);
    }
    switch (labelError.reason) {
      case "invalid_name":
        return t("labels.invalidName");
      case "name_conflict":
        return t("labels.nameConflict");
      case "catalog_limit":
        return t("labels.catalogLimit");
      case "project_not_found":
        return t("labels.projectMissing");
      case "label_not_found":
        return t("labels.labelMissing");
      case "task_not_found":
      case "wrong_project":
      case "invalid_filter":
      case "invalid_mutation":
        return t("labels.mutationFailed");
    }
  };
  const preparedSearch = search.trim().normalize("NFC");
  const labels = useMemo(
    () => catalog.data?.labels.filter((label) => labelNameContains(label.name, preparedSearch)) ?? [],
    [catalog.data, preparedSearch],
  );
  const canCreate =
    preparedSearch.length > 0 && !labels.some((label) => labelNamesEqual(label.name, preparedSearch));
  const catalogAtLimit = (catalog.data?.labels.length ?? 0) >= workflowLabelMaxIDs;
  const unlabeledName = t("labels.unlabeled");
  const showUnlabeledChoice =
    invocation.kind === "filter" &&
    (catalog.data?.labels.length ?? 0) > 0 &&
    labelNameContains(unlabeledName, preparedSearch);
  const choices = useMemo<readonly LabelChooserChoice[]>(
    () => [
      ...(showUnlabeledChoice ? ([{ kind: "unlabeled" }] as const) : []),
      ...labels.map((label) => ({ kind: "label" as const, label })),
    ],
    [labels, showUnlabeledChoice],
  );
  const choiceCount = choices.length;
  const createError = mutations.create.isError ? mutationErrorMessage(mutations.create.error) : null;

  const createLabel = async () => {
    try {
      const label = await mutations.create.mutateAsync(preparedSearch);
      selectLabel(invocation, label.id, true);
      setSearch("");
      setKeyboardHighlightedIndex(null);
      mutations.create.reset();
    } catch {
      // The mutation owns the visible error state.
    }
  };
  const activateChoice = (index: number) => {
    const choice = choices[index];
    if (choice === undefined) {
      return;
    }
    if (choice.kind === "unlabeled") {
      selectUnlabeled(invocation);
      return;
    }
    const selection = labelResultRowSelection(invocation, choice.label.id);
    selectLabel(invocation, choice.label.id, selection.kind === "binary" ? !selection.selected : true);
  };
  const hasSearchAction = choiceCount > 0 || (canCreate && !catalogAtLimit && !mutations.create.isPending);
  const runSearchAction = () => {
    if (choiceCount > 0) {
      activateChoice(Math.min(keyboardHighlightedIndex ?? 0, choiceCount - 1));
      return;
    }
    if (canCreate && !catalogAtLimit && !mutations.create.isPending) {
      void createLabel();
    }
  };
  const searchShortcut = useTextFieldSubmitShortcut({
    action: runSearchAction,
    available: hasSearchAction,
    kind: "direct",
  });
  const handleSearchKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (handleLabelChoiceNavigation(event, choiceCount, setKeyboardHighlightedIndex)) {
      return;
    }
    if (isTextFieldSubmitShortcut(event, nativeBridge.capabilities.platform)) {
      searchShortcut(event);
      return;
    }
    if (event.metaKey || event.ctrlKey) {
      return;
    }
    if (event.key !== "Enter") {
      return;
    }
    if (choiceCount > 0) {
      event.preventDefault();
      runSearchAction();
      return;
    }
    if (canCreate && !catalogAtLimit && !mutations.create.isPending) {
      event.preventDefault();
      runSearchAction();
    }
  };
  const commitRename = async () => {
    if (rename === null || rename.pending) {
      return;
    }
    const current = rename;
    setRename({ ...current, error: null, pending: true });
    try {
      await mutations.rename.mutateAsync({
        labelID: current.labelID,
        name: current.draft,
      });
      setRename((latest) => (latest?.labelID === current.labelID ? null : latest));
    } catch (error) {
      setRename((latest) =>
        latest?.labelID === current.labelID
          ? {
              ...latest,
              error: mutationErrorMessage(error),
              pending: false,
            }
          : latest,
      );
    }
  };
  const confirmDelete = async () => {
    if (deletion === null || deletion.pending) {
      return;
    }
    const current = deletion;
    setDeletion({ ...current, error: null, pending: true });
    try {
      await mutations.delete.mutateAsync(current.labelID);
      removeDeletedSelection(invocation, current.labelID);
      setDeletion((latest) => (latest?.labelID === current.labelID ? null : latest));
    } catch (error) {
      setDeletion((latest) =>
        latest?.labelID === current.labelID
          ? {
              ...latest,
              error: mutationErrorMessage(error),
              pending: false,
            }
          : latest,
      );
    }
  };
  return (
    <Popover
      onOpenChange={(nextOpen) => {
        if (!nextOpen && (rename !== null || deletion !== null) && !outsideInteractionRef.current) {
          setRename(null);
          setDeletion(null);
          return;
        }
        outsideInteractionRef.current = false;
        setOpen(nextOpen);
        if (!nextOpen) {
          setSearch("");
          setKeyboardHighlightedIndex(null);
          setRename(null);
          setDeletion(null);
          mutations.create.reset();
          mutations.delete.reset();
          mutations.rename.reset();
        }
      }}
      open={open}
    >
      <PopoverTrigger asChild>{trigger}</PopoverTrigger>
      <PopoverContent
        align="start"
        className="w-[min(25.3rem,calc(100vw-24px))] gap-[var(--space-2)] p-[var(--space-2)]"
        collisionPadding={12}
        level={3}
        onEscapeKeyDown={(event) => {
          if (rename === null && deletion === null) {
            return;
          }
          event.preventDefault();
          setRename(null);
          setDeletion(null);
        }}
        onPointerDownOutside={() => {
          outsideInteractionRef.current = true;
        }}
      >
        {renderLabelChooserSearch({
          canCreate,
          catalogAtLimit,
          choiceCount,
          createError,
          createPending: mutations.create.isPending,
          invocation,
          onCreate() {
            void createLabel();
          },
          onKeyDown: handleSearchKeyDown,
          onSearchChange(value) {
            setSearch(value);
            setKeyboardHighlightedIndex(null);
            mutations.create.reset();
          },
          preparedSearch,
          search,
          searchErrorID,
          t,
        })}
        {renderLabelChooserResults({
          catalog,
          choices,
          confirmDelete,
          commitRename,
          deletion,
          invocation,
          keyboardHighlightedIndex,
          rename,
          setDeletion,
          setKeyboardHighlightedIndex,
          setRename,
          t,
          unlabeledName,
        })}
      </PopoverContent>
    </Popover>
  );
}

function handleLabelChoiceNavigation(
  event: KeyboardEvent<HTMLInputElement>,
  choiceCount: number,
  setHighlightedIndex: (update: (current: number | null) => number) => void,
): boolean {
  if (choiceCount === 0) {
    return false;
  }
  if (event.key === "ArrowDown") {
    event.preventDefault();
    setHighlightedIndex((current) => (current === null ? 0 : (current + 1) % choiceCount));
    return true;
  }
  if (event.key === "ArrowUp") {
    event.preventDefault();
    setHighlightedIndex((current) =>
      current === null ? choiceCount - 1 : (current - 1 + choiceCount) % choiceCount,
    );
    return true;
  }
  return false;
}

function renderLabelChooserSearch({
  canCreate,
  catalogAtLimit,
  choiceCount,
  createError,
  createPending,
  invocation,
  onCreate,
  onKeyDown,
  onSearchChange,
  preparedSearch,
  search,
  searchErrorID,
  t,
}: Readonly<{
  canCreate: boolean;
  catalogAtLimit: boolean;
  choiceCount: number;
  createError: string | null;
  createPending: boolean;
  invocation: LabelChooserInvocation;
  onCreate(): void;
  onKeyDown(event: KeyboardEvent<HTMLInputElement>): void;
  onSearchChange(value: string): void;
  preparedSearch: string;
  search: string;
  searchErrorID: string;
  t: TFunction;
}>) {
  return (
    <div className="flex items-start gap-[var(--space-2)]">
      <div className="grid min-w-0 flex-1 gap-[var(--space-2)]">
        <span className="relative block">
          <SearchIcon
            aria-hidden="true"
            className="pointer-events-none absolute top-1/2 left-[var(--space-2)] -translate-y-1/2 text-[var(--color-muted)]"
            size={16}
            strokeWidth={1.8}
          />
          <input
            aria-describedby={createError === null ? undefined : searchErrorID}
            aria-invalid={createError === null ? undefined : true}
            aria-label={t("labels.search")}
            autoComplete="off"
            className={`${fieldInputClassName} text-sm`}
            onChange={(event) => {
              onSearchChange(event.currentTarget.value);
            }}
            onKeyDown={onKeyDown}
            inputMode="search"
            style={{
              height: "var(--space-6)",
              paddingBlock: "var(--space-0)",
              paddingInlineEnd: "calc(var(--space-6) + var(--space-1))",
              paddingInlineStart: "calc(var(--space-2) + var(--space-4) + var(--space-1))",
            }}
            type="text"
            value={search}
          />
          {canCreate ? (
            <span className="absolute top-1/2 right-[var(--space-1)] -translate-y-1/2">
              <IconTooltipButton
                disabled={catalogAtLimit || createPending}
                label={
                  catalogAtLimit ? t("labels.catalogLimit") : t("labels.create", { name: preparedSearch })
                }
                onClick={onCreate}
                size="icon-sm"
              >
                <PlusIcon aria-hidden="true" size={14} strokeWidth={1.8} />
              </IconTooltipButton>
            </span>
          ) : null}
        </span>
        {choiceCount === 0 && canCreate && !catalogAtLimit ? (
          <span className="px-[var(--space-2)] py-[var(--space-1)] text-sm leading-relaxed text-[var(--color-muted)]">
            {t("labels.noMatchesCreateHint")}
          </span>
        ) : null}
        {createError === null ? null : (
          <span
            className="px-[var(--space-1)] text-xs text-[var(--color-error)]"
            id={searchErrorID}
            role="alert"
          >
            {createError}
          </span>
        )}
      </div>
      {invocation.kind === "filter" ? (
        <SegmentedControl
          ariaLabel={t("labels.matchMode")}
          className="shrink-0"
          disabled={invocation.state.filter.kind === "unlabeled"}
          onValueChange={(mode) => {
            invocation.onAction({ type: "named.mode", mode });
          }}
          options={[
            { label: t("labels.matchAny"), value: "any" },
            { label: t("labels.matchAll"), value: "all" },
          ]}
          value={invocation.state.namedMode}
        />
      ) : null}
    </div>
  );
}

function renderLabelChooserResults({
  catalog,
  choices,
  confirmDelete,
  commitRename,
  deletion,
  invocation,
  keyboardHighlightedIndex,
  rename,
  setDeletion,
  setKeyboardHighlightedIndex,
  setRename,
  t,
  unlabeledName,
}: Readonly<{
  catalog: ReturnType<typeof useProjectLabelCatalog>;
  choices: readonly LabelChooserChoice[];
  confirmDelete(): Promise<void>;
  commitRename(): Promise<void>;
  deletion: DeleteState | null;
  invocation: LabelChooserInvocation;
  keyboardHighlightedIndex: number | null;
  rename: RenameState | null;
  setDeletion: Dispatch<SetStateAction<DeleteState | null>>;
  setKeyboardHighlightedIndex: Dispatch<SetStateAction<number | null>>;
  setRename: Dispatch<SetStateAction<RenameState | null>>;
  t: TFunction;
  unlabeledName: string;
}>) {
  if (catalog.isPending) {
    return (
      <div className="grid min-h-20 place-items-center" role="status">
        <Spinner />
      </div>
    );
  }
  if (catalog.isError) {
    return (
      <div className="grid gap-[var(--space-2)] p-[var(--space-2)] text-sm text-[var(--color-error)]">
        <span>{t("labels.loadFailed")}</span>
        <Button
          onClick={() => {
            void catalog.refetch();
          }}
          variant="primary"
        >
          {t("app.retry")}
        </Button>
      </div>
    );
  }
  return (
    <div
      className="grid max-h-[min(calc(10*2.25rem+9*var(--space-1)),calc(var(--radix-popover-content-available-height)-10rem))] gap-[var(--space-1)] overflow-y-auto overscroll-contain pr-[var(--space-1)]"
      onPointerMove={() => {
        setKeyboardHighlightedIndex(null);
      }}
      role="list"
    >
      {choices.map((choice, index) =>
        renderLabelChooserChoiceRow({
          choice,
          confirmDelete,
          commitRename,
          deletion,
          highlighted: index === keyboardHighlightedIndex,
          invocation,
          rename,
          setDeletion,
          setRename,
          unlabeledName,
        }),
      )}
    </div>
  );
}

function renderLabelChooserChoiceRow({
  choice,
  confirmDelete,
  commitRename,
  deletion,
  highlighted,
  invocation,
  rename,
  setDeletion,
  setRename,
  unlabeledName,
}: Readonly<{
  choice: LabelChooserChoice;
  confirmDelete(): Promise<void>;
  commitRename(): Promise<void>;
  deletion: DeleteState | null;
  highlighted: boolean;
  invocation: LabelChooserInvocation;
  rename: RenameState | null;
  setDeletion: Dispatch<SetStateAction<DeleteState | null>>;
  setRename: Dispatch<SetStateAction<RenameState | null>>;
  unlabeledName: string;
}>) {
  if (choice.kind === "unlabeled") {
    return (
      <UnlabeledResultRow
        highlighted={highlighted}
        key="unlabeled"
        name={unlabeledName}
        onSelect={() => {
          selectUnlabeled(invocation);
        }}
        selected={invocation.kind === "filter" && invocation.state.filter.kind === "unlabeled"}
      />
    );
  }
  const { label } = choice;
  if (rename?.labelID === label.id) {
    return (
      <LabelRenameEditor
        key={label.id}
        onCancel={() => {
          setRename(null);
        }}
        onChange={(draft) => {
          setRename({ ...rename, draft, error: null });
        }}
        onCommit={() => {
          void commitRename();
        }}
        rename={rename}
      />
    );
  }
  const selection = labelResultRowSelection(invocation, label.id);
  const labelDeletion = deletion?.labelID === label.id ? deletion : null;
  return (
    <LabelResultRow
      deletion={labelDeletion}
      highlighted={highlighted}
      key={label.id}
      label={label}
      onDeleteConfirm={() => {
        void confirmDelete();
      }}
      onDeleteOpenChange={(nextOpen) => {
        if (nextOpen) {
          setDeletion({
            labelID: label.id,
            error: null,
            pending: false,
          });
          return;
        }
        setDeletion((current) => (current?.labelID === label.id && current.pending ? current : null));
      }}
      onRename={() => {
        setRename({
          labelID: label.id,
          draft: label.name,
          error: null,
          pending: false,
        });
      }}
      onSelect={() => {
        selectLabel(invocation, label.id, selection.kind === "binary" ? !selection.selected : true);
      }}
      selection={selection}
    />
  );
}

function labelResultRowSelection(
  invocation: LabelChooserInvocation,
  labelID: string,
): LabelResultRowSelection {
  if (invocation.kind === "assignment") {
    return {
      kind: "binary",
      selected: invocation.selectedLabelIDs.includes(labelID),
    };
  }
  return {
    kind: "condition",
    state: labelFilterCondition(invocation.state, labelID),
  };
}

function labelFilterCondition(state: LabelFilterState, labelID: string): LabelFilterCondition {
  if (state.filter.kind !== "named") {
    return "neutral";
  }
  if (state.filter.labelIDs.includes(labelID)) {
    return "included";
  }
  return state.filter.excludedLabelIDs.includes(labelID) ? "excluded" : "neutral";
}

function selectLabel(invocation: LabelChooserInvocation, labelID: string, selected: boolean): void {
  if (invocation.kind === "filter") {
    invocation.onAction({ type: "named.cycle", labelID });
    return;
  }
  invocation.onSelectionChange(labelID, selected);
}

function selectUnlabeled(invocation: LabelChooserInvocation): void {
  if (invocation.kind === "filter") {
    invocation.onAction({ type: "unlabeled.toggle" });
  }
}

function removeDeletedSelection(invocation: LabelChooserInvocation, labelID: string): void {
  if (invocation.kind === "filter") {
    invocation.onAction({ type: "label.deleted", labelID });
    return;
  }
  if (invocation.selectedLabelIDs.includes(labelID)) {
    invocation.onSelectionChange(labelID, false);
  }
}
