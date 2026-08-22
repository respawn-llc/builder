export function timestampMillis(timestamp: Readonly<{ seconds: bigint; nanos: number }>): number {
  return Number(timestamp.seconds) * 1000 + timestamp.nanos / 1_000_000;
}
