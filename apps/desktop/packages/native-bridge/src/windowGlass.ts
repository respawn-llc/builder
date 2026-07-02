export type NativeWindowGlassTint = Readonly<{
  red: number;
  green: number;
  blue: number;
  alpha: number;
}>;

const nativeWindowGlassTintChannels = ["red", "green", "blue", "alpha"] as const;

export function validateNativeWindowGlassTint(tint: NativeWindowGlassTint | null): void {
  if (tint === null) {
    return;
  }
  for (const channel of nativeWindowGlassTintChannels) {
    const value = tint[channel];
    if (!Number.isFinite(value) || value < 0 || value > 1) {
      throw new Error(`Native glass tint ${channel} channel must be a finite number from 0 to 1.`);
    }
  }
}
