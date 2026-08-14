import { Component, type AnimationEvent, type ReactNode } from "react";

import { cx } from "@/ui";

type CrossfadeLayer = Readonly<{
  children: ReactNode;
  contentKey: string;
}>;

type OverlappingCrossfadeProps = Readonly<{
  children: ReactNode;
  contentKey: string;
}>;

type OverlappingCrossfadeState = Readonly<{
  current: CrossfadeLayer;
  outgoing: CrossfadeLayer | null;
}>;

const crossfadeCleanupFallbackMs = 1_000;

export class OverlappingCrossfade extends Component<OverlappingCrossfadeProps, OverlappingCrossfadeState> {
  override state: OverlappingCrossfadeState = {
    current: {
      children: this.props.children,
      contentKey: this.props.contentKey,
    },
    outgoing: null,
  };

  private cleanupTimer: ReturnType<typeof setTimeout> | null = null;
  private outgoingElement: HTMLDivElement | null = null;

  static getDerivedStateFromProps(
    props: OverlappingCrossfadeProps,
    state: OverlappingCrossfadeState,
  ): OverlappingCrossfadeState {
    const current = { children: props.children, contentKey: props.contentKey };
    if (state.current.contentKey === props.contentKey) {
      return { ...state, current };
    }
    return {
      current,
      outgoing: state.current,
    };
  }

  override componentDidMount(): void {
    this.scheduleCleanup();
  }

  override componentDidUpdate(
    _previousProps: OverlappingCrossfadeProps,
    previousState: OverlappingCrossfadeState,
  ): void {
    if (previousState.outgoing?.contentKey !== this.state.outgoing?.contentKey) {
      this.scheduleCleanup();
    }
  }

  override componentWillUnmount(): void {
    this.setOutgoingElement(null);
    this.clearCleanupTimer();
  }

  override render(): ReactNode {
    const { current, outgoing } = this.state;
    return (
      <div className="relative h-full min-h-0">
        {outgoing === null ? null : (
          <div
            aria-hidden="true"
            className="pointer-events-none absolute inset-0 z-0 animate-[detail-pane-crossfade-out_var(--motion-normal)_both]"
            data-crossfade-content-key={outgoing.contentKey}
            key={outgoing.contentKey}
            onAnimationEnd={this.finishOutgoing}
            ref={this.setOutgoingElement}
          >
            {outgoing.children}
          </div>
        )}
        <div
          className={cx(
            "absolute inset-0 z-10",
            outgoing !== null && "animate-[detail-pane-crossfade-in_var(--motion-normal)_both]",
          )}
          key={current.contentKey}
        >
          {current.children}
        </div>
      </div>
    );
  }

  private finishOutgoing = (event: AnimationEvent<HTMLDivElement>): void => {
    if (event.target === event.currentTarget) {
      this.clearOutgoing(event.currentTarget.dataset.crossfadeContentKey);
    }
  };

  private finishCancelledOutgoing = (event: Event): void => {
    const element = event.currentTarget;
    if (element instanceof HTMLDivElement && event.target === element) {
      this.clearOutgoing(element.dataset.crossfadeContentKey);
    }
  };

  private setOutgoingElement = (element: HTMLDivElement | null): void => {
    if (this.outgoingElement === element) {
      return;
    }
    this.outgoingElement?.removeEventListener("animationcancel", this.finishCancelledOutgoing);
    this.outgoingElement = element;
    this.outgoingElement?.addEventListener("animationcancel", this.finishCancelledOutgoing);
  };

  private scheduleCleanup(): void {
    this.clearCleanupTimer();
    const outgoingKey = this.state.outgoing?.contentKey;
    if (outgoingKey === undefined) {
      return;
    }
    this.cleanupTimer = setTimeout(() => {
      this.clearOutgoing(outgoingKey);
    }, crossfadeCleanupFallbackMs);
  }

  private clearOutgoing(contentKey: string | undefined): void {
    if (contentKey === undefined) {
      return;
    }
    this.setState((state) =>
      state.outgoing?.contentKey === contentKey ? { ...state, outgoing: null } : null,
    );
  }

  private clearCleanupTimer(): void {
    if (this.cleanupTimer !== null) {
      clearTimeout(this.cleanupTimer);
      this.cleanupTimer = null;
    }
  }
}
