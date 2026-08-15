'use strict';

const requiredMajor = 22;
const detected = process.versions && process.versions.node;
const major = Number(String(detected || '').split('.')[0]);

if (!Number.isInteger(major) || major < requiredMajor) {
  console.error(
    `Kent developer commands require Node.js ${requiredMajor} or newer; detected ${detected || 'an unknown version'}.`,
  );
  process.exit(2);
}
