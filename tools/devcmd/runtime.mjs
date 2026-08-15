import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { constants as fsConstants, createReadStream } from "node:fs";
import { access, mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, delimiter, dirname, extname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs as nodeParseArgs } from "node:util";

const moduleDirectory = dirname(fileURLToPath(import.meta.url));

export const repoRoot = resolve(moduleDirectory, "..", "..");

export const hostPlatform = Object.freeze({
  darwin: process.platform === "darwin",
  linux: process.platform === "linux",
  windows: process.platform === "win32",
  name: process.platform,
});

export class CommandError extends Error {
  constructor(message, exitCode = 1) {
    super(message);
    this.exitCode = exitCode;
  }
}

export function parseArgs(options) {
  try {
    return nodeParseArgs({ strict: true, allowPositionals: false, ...options });
  } catch (error) {
    throw new CommandError(error.message, 2);
  }
}

export function usageError(message) {
  throw new CommandError(message, 2);
}

export function requirePlatform(predicate, message) {
  if (!predicate(hostPlatform)) {
    throw new CommandError(message, 2);
  }
}

export async function requireExecutable(name, guidance) {
  const executable = await findExecutable(name);
  if (executable) {
    return executable;
  }
  throw new CommandError(
    guidance ? `${name} is required. ${guidance}` : `${name} is required.`,
    2,
  );
}

export async function findExecutable(name, environment = process.env) {
  if (isAbsolute(name) || basename(name) !== name) {
    return (await isExecutable(name)) ? name : undefined;
  }
  const extensions =
    hostPlatform.windows && extname(name) === ""
      ? (environment.PATHEXT || ".COM;.EXE;.BAT;.CMD").split(delimiter)
      : [""];
  for (const directory of (environment.PATH || "").split(delimiter)) {
    if (!directory) continue;
    for (const extension of extensions) {
      const candidate = join(directory, `${name}${extension}`);
      if (await isExecutable(candidate)) return candidate;
    }
  }
  return undefined;
}

async function isExecutable(path) {
  try {
    await access(path, fsConstants.X_OK);
    return true;
  } catch {
    return false;
  }
}

export async function readVersion() {
  return normalizeVersion((await readFile(join(repoRoot, "VERSION"), "utf8")).trim());
}

export function normalizeVersion(version) {
  return version[0] === "v" ? version.slice(1) : version;
}

export async function withTemporaryDirectory(prefix, operation) {
  const directory = await mkdtemp(join(tmpdir(), prefix));
  try {
    return await operation(directory);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

export async function run(command, args = [], options = {}) {
  const {
    cwd = repoRoot,
    env = process.env,
    stdio = "inherit",
    detached = !hostPlatform.windows,
    allowFailure = false,
    input,
  } = options;

  const invocation = await resolveInvocation(command, args, env);
  const child = spawn(invocation.command, invocation.args, {
    cwd,
    env,
    stdio: input === undefined ? stdio : ["pipe", "inherit", "inherit"],
    shell: false,
    detached,
    windowsHide: true,
  });
  if (input !== undefined) child.stdin.end(input);
  return waitForChild(child, command, allowFailure);
}

export async function capture(command, args = [], options = {}) {
  const {
    cwd = repoRoot,
    env = process.env,
    stderr = "inherit",
    allowFailure = false,
  } = options;
  const invocation = await resolveInvocation(command, args, env);
  const child = spawn(invocation.command, invocation.args, {
    cwd,
    env,
    stdio: ["ignore", "pipe", stderr],
    shell: false,
    detached: !hostPlatform.windows,
    windowsHide: true,
  });
  let output = "";
  child.stdout.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    output += chunk;
  });
  await waitForChild(child, command, allowFailure);
  return output;
}

async function resolveInvocation(command, args, environment) {
  const executable = await findExecutable(command, environment);
  if (!executable) throw new CommandError(`${command} is required.`, 2);
  const extension = extname(executable).toLowerCase();
  if (hostPlatform.windows && (extension === ".cmd" || extension === ".bat")) {
    const commandInterpreter =
      environment.ComSpec ||
      environment.COMSPEC ||
      (await findExecutable("cmd.exe", environment));
    if (!commandInterpreter) throw new CommandError("cmd.exe is required.", 2);
    return {
      command: commandInterpreter,
      args: ["/d", "/c", executable, ...args],
    };
  }
  return { command: executable, args };
}

export async function fileSha256(path) {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(path)) hash.update(chunk);
  return hash.digest("hex");
}

async function waitForChild(child, command, allowFailure) {
  let forwardedSignal;
  const forwarders = new Map();
  const forward = (signal) => {
    forwardedSignal = signal;
    signalChild(child, signal);
  };
  const signals = ["SIGINT", "SIGTERM"];
  for (const signal of signals) {
    const forwarder = () => forward(signal);
    forwarders.set(signal, forwarder);
    process.once(signal, forwarder);
  }

  try {
    const result = await new Promise((resolveResult, reject) => {
      child.once("error", reject);
      child.once("exit", (code, signal) => resolveResult({ code, signal }));
    });
    if (result.code === 0 || allowFailure) return result;
    if (forwardedSignal || result.signal) {
      const signal = forwardedSignal || result.signal;
      throw new CommandError(
        `${command} interrupted by ${signal}`,
        signal === "SIGINT" ? 130 : 143,
      );
    }
    throw new CommandError(`${command} exited with status ${result.code}`, result.code || 1);
  } finally {
    for (const signal of signals) {
      process.removeListener(signal, forwarders.get(signal));
    }
  }
}

function signalChild(child, signal) {
  if (!child.pid || child.exitCode !== null) return;
  try {
    if (hostPlatform.windows) {
      child.kill(signal);
    } else {
      process.kill(-child.pid, signal);
    }
  } catch (error) {
    if (error.code !== "ESRCH") throw error;
  }
}

export async function main(operation) {
  try {
    await operation();
  } catch (error) {
    if (error instanceof CommandError) {
      console.error(error.message);
      process.exitCode = error.exitCode;
      return;
    }
    throw error;
  }
}
