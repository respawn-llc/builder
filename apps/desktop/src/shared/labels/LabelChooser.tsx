import type { TFunction } from "i18next";
import { PlusIcon, SearchIcon } from "lucide-react";
import type { KeyboardEvent } from "react";
import {
  useId,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type ReactElement,
  type SetStateAction,
} from "react";
import { useTranslation } from "react-i18next";

import { ReorderableList, type ReorderableListItemRenderProps } from "@app/ui-kit";
import { errorMessage, workflowLabelMaxIDs, type ProjectLabel } from "@/api";
import { useAppServices, useStatusController } from "@/app-facade";
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
  type RenameState,
} from "./LabelChooserRows";
import { labelNameContains, labelNamesEqual } from "./labelComparison";
import type { LabelFilterAction, LabelFilterState } from "./labelFilterState";
import {
  handleLabelChooserSearchKeyDown,
  labelResultRowSelection,
  selectLabel,
  selectUnlabeled,
  useLabelChooserMutationActions,
} from "./labelChooserActions";
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
      onLabelCreated?(labelID: string): void;
      onCreatePendingChange?(pending: boolean): void;
      onSelectionChange(labelID: string, selected: boolean): void;
    }>;

export type LabelChooserProps = Readonly<{
  invocation: LabelChooserInvocation;
  trigger: ReactElement;
}>;

type LabelChooserChoice = Readonly<{ kind: "unlabeled" }> | Readonly<{ kind: "label"; label: ProjectLabel }>;

function renderLabelChooserSearch({
  canCreate,
  catalogAtLimit,
  choiceCount,
  createError,
  catalogMutationPending,
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
  catalogMutationPending: boolean;
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
                disabled={catalogAtLimit || catalogMutationPending}
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

export function LabelChooser({ invocation, trigger }: LabelChooserProps) {
  const { t } = useTranslation();
  const { nativeBridge } = useAppServices();
  const { push } = useStatusController();
  const catalog = useProjectLabelCatalog();
  const mutations = useProjectLabelCatalogMutations();
  const [search, setSearch] = useState("");
  const [keyboardHighlightedIndex, setKeyboardHighlightedIndex] = useState<number | null>(null);
  const [open, setOpen] = useState(false);
  const [rename, setRename] = useState<RenameState | null>(null);
  const [deletion, setDeletion] = useState<DeleteState | null>(null);
  const outsideInteractionRef = useRef(false);
  const searchErrorID = useId();
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
  const choices: readonly LabelChooserChoice[] = [
    ...(showUnlabeledChoice ? ([{ kind: "unlabeled" }] as const) : []),
    ...labels.map((label) => ({ kind: "label" as const, label })),
  ];
  const choiceCount = choices.length;
  const { catalogMutationPending, commitRename, confirmDelete, createError, createLabel } =
    useLabelChooserMutationActions({
      deletion,
      invocation,
      mutations,
      preparedSearch,
      rename,
      setDeletion,
      setKeyboardHighlightedIndex,
      setRename,
      setSearch,
      t,
    });
  const reorderEnabled = invocation.kind === "filter" && preparedSearch.length === 0 && labels.length >= 2;
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
          catalogMutationPending,
          invocation,
          onCreate() {
            void createLabel();
          },
          onKeyDown(event) {
            handleLabelChooserSearchKeyDown({
              canCreate,
              catalogMutationPending,
              catalogAtLimit,
              createLabel,
              event,
              highlightedIndex: keyboardHighlightedIndex,
              invocation,
              platform: nativeBridge.capabilities.platform,
              choices,
              setHighlightedIndex: setKeyboardHighlightedIndex,
            });
          },
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
          showUnlabeledChoice,
          labels,
          onReorder(nextLabels) {
            void mutations.reorder.mutateAsync(nextLabels.map((label) => label.id)).catch((error: unknown) => {
              push({
                body: errorMessage(error),
                durationMs: Infinity,
                id: "project-label-reorder-error",
                title: t("labels.mutationFailed"),
                tone: "danger",
              });
            });
          },
          reorderEnabled,
          catalogMutationPending,
        })}
      </PopoverContent>
    </Popover>
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
  showUnlabeledChoice,
  labels,
  onReorder,
  reorderEnabled,
  catalogMutationPending,
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
  showUnlabeledChoice: boolean;
  labels: readonly ProjectLabel[];
  onReorder(nextLabels: readonly ProjectLabel[]): void;
  reorderEnabled: boolean;
  catalogMutationPending: boolean;
}>) {
  const labelIndexes = new Map(labels.map((label, index) => [label.id, index]));
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
      {reorderEnabled && showUnlabeledChoice ? (
        <UnlabeledResultRow
          highlighted={keyboardHighlightedIndex === 0}
          name={unlabeledName}
          onSelect={() => {
            selectUnlabeled(invocation);
          }}
          selected={invocation.kind === "filter" && invocation.state.filter.kind === "unlabeled"}
        />
      ) : null}
      {reorderEnabled ? (
        <ReorderableList
          disabled={catalogMutationPending}
          getItemID={(label) => label.id}
          items={labels}
          onCommit={({ items }) => {
            onReorder(items);
          }}
          renderItem={(label, sortable) =>
            renderLabelChooserChoiceRow({
              choice: { kind: "label", label },
              confirmDelete,
              commitRename,
              deletion,
              highlighted:
                keyboardHighlightedIndex ===
                (labelIndexes.get(label.id) ?? -1) + (showUnlabeledChoice ? 1 : 0),
              invocation,
              rename,
              setDeletion,
              setRename,
              unlabeledName,
              catalogMutationPending,
              sortable,
            })
          }
        />
      ) : (
        choices.map((choice, index) =>
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
            catalogMutationPending,
          }),
        )
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
  catalogMutationPending,
  sortable,
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
  catalogMutationPending: boolean;
  sortable?: ReorderableListItemRenderProps | undefined;
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
        catalogMutationPending={catalogMutationPending}
        rename={rename}
      />
    );
  }
  const selection = labelResultRowSelection(invocation, label.id);
  const labelDeletion = deletion?.labelID === label.id ? deletion : null;
  const row = (
    <LabelResultRow
      catalogMutationPending={catalogMutationPending}
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
      reorder={sortable}
      selection={selection}
    />
  );
  if (sortable === undefined) {
    return row;
  }
  return (
    <div data-testid={`label-reorder-item-${label.id}`} ref={sortable.itemRef} style={sortable.style}>
      {row}
    </div>
  );
}
