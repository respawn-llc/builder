export type DesktopProcess = Readonly<{
  id: string;
  state: string;
  command: string;
  workdir: string;
  startedAt: number;
  finishedAt: number | null;
  exitCode: number | null;
  recentOutput: string;
  running: boolean;
  killRequested: boolean;
}>;
