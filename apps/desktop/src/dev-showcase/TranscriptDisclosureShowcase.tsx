import {
  Activity,
  CircleAlert,
  Copy,
  FileText,
  FolderOpen,
  ShieldCheck,
} from "lucide-react";
import { StrictMode, useEffect, useState, type ReactNode } from "react";
import { createRoot } from "react-dom/client";

import {
  TranscriptDisclosure,
  type TranscriptDisclosureIconTone,
  type TranscriptDisclosureProps,
} from "@/ui";

type ShowcaseTheme = "system" | "light" | "dark";
type ShowcaseWidth = "narrow" | "wide";

const toneIcons: Readonly<Record<TranscriptDisclosureIconTone, ReactNode>> = {
  neutral: <FileText aria-hidden="true" size={16} />,
  warning: <CircleAlert aria-hidden="true" size={16} />,
  error: <CircleAlert aria-hidden="true" size={16} />,
  success: <ShieldCheck aria-hidden="true" size={16} />,
};

const actionClassName =
  "inline-flex min-h-7 items-center gap-[var(--space-1)] rounded-[var(--radius-s)] border border-transparent bg-transparent px-[var(--space-1)] text-xs text-[var(--color-muted)] hover:bg-[var(--color-island-2)] hover:text-[var(--color-on-background)] focus-visible:outline focus-visible:outline-2 focus-visible:outline-[var(--color-primary)]";

export function TranscriptDisclosureShowcase() {
  const [theme, setTheme] = useState<ShowcaseTheme>("system");
  const [width, setWidth] = useState<ShowcaseWidth>("wide");
  const [reducedMotion, setReducedMotion] = useState(false);

  useEffect(() => {
    if (theme === "system") {
      document.documentElement.removeAttribute("data-theme");
      return;
    }
    document.documentElement.setAttribute("data-theme", theme);
  }, [theme]);

  useEffect(() => {
    const originalMatchMedia = window.matchMedia;
    const rootStyle = document.documentElement.style;
    const previousMotionFast = rootStyle.getPropertyValue("--motion-fast");
    const previousMotionFastPriority = rootStyle.getPropertyPriority("--motion-fast");
    window.matchMedia = (query) =>
      query === "(prefers-reduced-motion: reduce)"
        ? createMediaQueryList(query, reducedMotion)
        : originalMatchMedia(query);
    if (reducedMotion) {
      rootStyle.setProperty("--motion-fast", "0ms ease");
    }
    return () => {
      window.matchMedia = originalMatchMedia;
      if (previousMotionFast.length === 0) {
        rootStyle.removeProperty("--motion-fast");
      } else {
        rootStyle.setProperty("--motion-fast", previousMotionFast, previousMotionFastPriority);
      }
    };
  }, [reducedMotion]);

  return (
    <div className="h-full overflow-y-auto bg-[var(--color-background)] px-[var(--space-4)] py-[var(--space-5)] text-[var(--color-on-background)]">
      <div className="mx-auto grid w-full max-w-[1120px] gap-[var(--space-5)]">
        <header className="grid gap-[var(--space-3)]">
          <div>
            <p className="m-0 text-xs font-semibold uppercase tracking-[0.12em] text-[var(--color-muted)]">Development showcase</p>
            <h1 className="m-0 text-2xl font-semibold">Transcript disclosure</h1>
            <p className="m-0 max-w-[720px] text-sm text-[var(--color-muted)]">
              Temporary fixture catalog for the flat Desktop Chat transcript primitive.
            </p>
          </div>
          <ShowcaseControls
            reducedMotion={reducedMotion}
            setReducedMotion={setReducedMotion}
            setTheme={setTheme}
            setWidth={setWidth}
            theme={theme}
            width={width}
          />
        </header>

        <main className={width === "narrow" ? "mx-auto grid w-full max-w-[620px] gap-[var(--space-4)]" : "grid w-full gap-[var(--space-4)]"}>
          <ShowcaseStory title="Collapsed">
            <DisclosureStory defaultExpanded={false} iconTone="neutral" summary="Loaded context is available when needed." />
          </ShowcaseStory>
          <ShowcaseStory title="Expanded">
            <DisclosureStory defaultExpanded iconTone="neutral" summary="Expanded transcript content keeps its compact identity." />
          </ShowcaseStory>
          <ShowcaseStory title="Supplied default expanded">
            <DisclosureStory defaultExpanded iconTone="warning" summary="A row type can supply its mount default." />
          </ShowcaseStory>
          <ShowcaseStory title="Live">
            <TranscriptDisclosure
              body="This content is still changing while the live status remains visible."
              collapseLabel="Collapse live transcript item"
              defaultExpanded
              expandLabel="Expand live transcript item"
              icon={toneIcons.success}
              iconTone="success"
              liveStatus={<span className="text-xs text-[var(--color-success)]">Streaming</span>}
              summary="Live status and transcript content are independent."
              typeLabel="Live"
            />
          </ShowcaseStory>
          <ShowcaseStory title="Durable action">
            <DisclosureStory
              actions={
                <button className={actionClassName} onClick={() => undefined} type="button">
                  <Copy aria-hidden="true" size={14} />
                  Copy
                </button>
              }
              defaultExpanded
              iconTone="neutral"
              summary="Expanded durable rows expose ordinary caller-owned actions."
            />
          </ShowcaseStory>
          <ShowcaseStory title="Semantic icon tones">
            <div className="grid gap-[var(--space-2)]">
              {(["neutral", "warning", "error", "success"] satisfies TranscriptDisclosureIconTone[]).map((iconTone) => (
                <DisclosureStory
                  key={iconTone}
                  defaultExpanded={false}
                  iconTone={iconTone}
                  summary={`Leading icon tone: ${iconTone}`}
                  typeLabel={iconTone}
                />
              ))}
            </div>
          </ShowcaseStory>
          <ShowcaseStory title="Action versus header">
            <ActionVersusHeaderStory />
          </ShowcaseStory>
          <ShowcaseStory title="Unmount and remount reset">
            <RemountResetStory />
          </ShowcaseStory>
          <ShowcaseStory title="Long summary and body">
            <DisclosureStory
              body={<LongBody />}
              defaultExpanded
              iconTone="neutral"
              summary="A deliberately long compact summary demonstrates the header's single-line end ellipsis as the available width becomes narrow."
            />
          </ShowcaseStory>
        </main>
      </div>
    </div>
  );
}

function ShowcaseControls({
  reducedMotion,
  setReducedMotion,
  setTheme,
  setWidth,
  theme,
  width,
}: Readonly<{
  reducedMotion: boolean;
  setReducedMotion: (value: boolean) => void;
  setTheme: (value: ShowcaseTheme) => void;
  setWidth: (value: ShowcaseWidth) => void;
  theme: ShowcaseTheme;
  width: ShowcaseWidth;
}>) {
  return (
    <div className="flex flex-wrap items-center gap-[var(--space-3)] rounded-[var(--radius-m)] border border-[var(--color-outline)] bg-[var(--color-island-1)] p-[var(--space-3)] text-sm">
      <div className="flex items-center gap-[var(--space-1)]">
        <span className="text-xs font-semibold text-[var(--color-muted)]">Width</span>
        {(["narrow", "wide"] satisfies ShowcaseWidth[]).map((option) => (
          <button
            aria-pressed={width === option}
            className={actionClassName}
            key={option}
            onClick={() => {
              setWidth(option);
            }}
            type="button"
          >
            {option}
          </button>
        ))}
      </div>
      <div className="flex items-center gap-[var(--space-1)]">
        <span className="text-xs font-semibold text-[var(--color-muted)]">Theme</span>
        {(["system", "light", "dark"] satisfies ShowcaseTheme[]).map((option) => (
          <button
            aria-pressed={theme === option}
            className={actionClassName}
            key={option}
            onClick={() => {
              setTheme(option);
            }}
            type="button"
          >
            {option}
          </button>
        ))}
      </div>
      <label className="inline-flex items-center gap-[var(--space-1)] text-xs font-semibold text-[var(--color-muted)]">
        <input
          checked={reducedMotion}
          onChange={(event) => {
            setReducedMotion(event.currentTarget.checked);
          }}
          type="checkbox"
        />
        Reduced motion
      </label>
    </div>
  );
}

function ShowcaseStory({ children, title }: Readonly<{ children: ReactNode; title: string }>) {
  return (
    <section className="grid gap-[var(--space-2)]">
      <h2 className="m-0 text-sm font-semibold">{title}</h2>
      <div className="w-full overflow-hidden rounded-[var(--radius-m)] border border-[var(--color-outline)] bg-[var(--color-background)]">
        {children}
      </div>
    </section>
  );
}

function DisclosureStory({
  actions,
  body = "Full transcript content for this fixture.",
  defaultExpanded,
  iconTone,
  summary,
  typeLabel = "Transcript",
}: Readonly<{
  actions?: ReactNode;
  body?: ReactNode;
  defaultExpanded: boolean;
  iconTone: TranscriptDisclosureIconTone;
  summary: ReactNode;
  typeLabel?: ReactNode;
}>) {
  const props: TranscriptDisclosureProps = {
    actions,
    body,
    collapseLabel: "Collapse transcript item",
    defaultExpanded,
    expandLabel: "Expand transcript item",
    icon: toneIcons[iconTone],
    iconTone,
    summary,
    typeLabel,
  };
  return <TranscriptDisclosure {...props} />;
}

function ActionVersusHeaderStory() {
  const [actionCount, setActionCount] = useState(0);
  return (
    <DisclosureStory
      actions={
        <button
          className={actionClassName}
          onClick={() => {
            setActionCount((count) => count + 1);
          }}
          type="button"
        >
          <Activity aria-hidden="true" size={14} />
          Action {actionCount}
        </button>
      }
      defaultExpanded
      iconTone="neutral"
      summary="Activate the action without changing disclosure state."
    />
  );
}

function RemountResetStory() {
  const [mounted, setMounted] = useState(true);
  return (
    <div className="grid gap-[var(--space-2)]">
      {mounted ? (
        <TranscriptDisclosure
          body="This row returns to its default after remounting."
          collapseLabel="Collapse remount fixture"
          defaultExpanded={false}
          expandLabel="Expand remount fixture"
          icon={<FolderOpen aria-hidden="true" size={16} />}
          summary="Expand this row, unmount it, then mount it again."
          typeLabel="Context"
        />
      ) : null}
      <button
        className={`${actionClassName} justify-self-start`}
        onClick={() => {
          setMounted((value) => !value);
        }}
        type="button"
      >
        {mounted ? "Unmount row" : "Mount row"}
      </button>
    </div>
  );
}

function LongBody() {
  return (
    <div className="grid gap-[var(--space-2)]">
      {[
        "This body intentionally spans several lines so the virtualized row can measure changing height.",
        "The disclosure owns the flat full-width container; callers provide only transcript content.",
        "The primitive stays focused on flat disclosure mechanics and caller-owned content.",
      ].map((paragraph) => (
        <p className="m-0" key={paragraph}>
          {paragraph}
        </p>
      ))}
    </div>
  );
}

function createMediaQueryList(query: string, matches: boolean): MediaQueryList {
  return {
    matches,
    media: query,
    onchange: null,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    addListener: () => undefined,
    removeListener: () => undefined,
    dispatchEvent: () => false,
  };
}

const root = document.getElementById("root");

if (root === null) {
  throw new Error("Missing #root element");
}

createRoot(root).render(
  <StrictMode>
    <TranscriptDisclosureShowcase />
  </StrictMode>,
);
