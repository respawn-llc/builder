import type { DesktopProcess } from "@/api";
import { basename } from "@/app-facade";

export type ProcessStateTone = "error" | "muted" | "primary" | "success";
export type ProcessStateIndicator = "active" | "stopping" | "terminal";

export type ProcessPresentation = Readonly<{
  age: string | null;
  command: string;
  id: string;
  output: string;
  stateIndicator: ProcessStateIndicator;
  stateLabel: string;
  stateTone: ProcessStateTone;
  stopping: boolean;
  terminable: boolean;
  workdir: string;
}>;

export function projectProcessPresentation(
  process: DesktopProcess,
  observationTime: number,
  pendingTermination: boolean,
): ProcessPresentation {
  const stopping = process.killRequested || pendingTermination;
  const state = process.state.trim();
  return {
    age: processAge(process, observationTime),
    command: compactProcessCommand(process.command),
    id: process.id,
    output: latestNonemptyLine(process.recentOutput),
    stateIndicator: stopping ? "stopping" : processStateIndicator(process, state),
    stateLabel: processStateLabel(process, state),
    stateTone: processStateTone(process, state),
    stopping,
    terminable: process.running && !stopping,
    workdir: basename(process.workdir),
  };
}

function processStateLabel(process: DesktopProcess, state: string): string {
  if (state.length > 0) return state;
  if (process.running) return "running";
  if (process.exitCode === 0) return "completed";
  if (process.exitCode !== null) return "failed";
  return "queued";
}

function processStateTone(process: DesktopProcess, state: string): ProcessStateTone {
  if (state === "completed") return "success";
  if (state === "failed" || state === "killed") return "error";
  if (state === "starting" || state === "running" || process.running) return "primary";
  if (process.exitCode === 0) return "success";
  if (process.exitCode !== null) return "error";
  return "muted";
}

function processStateIndicator(process: DesktopProcess, state: string): ProcessStateIndicator {
  return state === "starting" || state === "running" || (state.length === 0 && process.running)
    ? "active"
    : "terminal";
}

function processAge(process: DesktopProcess, observationTime: number): string | null {
  const endTime = process.running ? observationTime : process.finishedAt;
  if (endTime === null) return null;
  const elapsedSeconds = Math.floor((endTime - process.startedAt) / 1_000);
  if (elapsedSeconds < 60) return `${elapsedSeconds.toString()}s`;
  const elapsedMinutes = Math.floor(elapsedSeconds / 60);
  if (elapsedMinutes < 60) return `${elapsedMinutes.toString()}m`;
  return `${Math.floor(elapsedMinutes / 60).toString()}h`;
}

function compactProcessCommand(command: string): string {
  const normalized = command.trim().replaceAll("\r\n", "\n");
  if (normalized.length === 0) return "tool call";
  const firstLine = normalized.split("\n", 1)[0]?.trim() ?? "";
  const commandText = (firstLine.split("\u001f", 1)[0] ?? "").trim();
  const preview = commandText.length > 0 ? commandText : "tool call";
  return normalized.includes("\n") && normalized !== preview ? `${preview} …` : preview;
}

function latestNonemptyLine(output: string): string {
  const lines = output.replaceAll("\r\n", "\n").split("\n");
  for (let index = lines.length - 1; index >= 0; index -= 1) {
    const line = lines[index]?.trim() ?? "";
    if (line.length > 0) return line;
  }
  return "";
}
