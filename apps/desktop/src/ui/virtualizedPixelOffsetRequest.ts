import { z } from "zod";

const schema = z.object({
  key: z.string().trim().min(1),
  offsetPx: z.number().nonnegative(),
});

export type VirtualizedPixelOffsetRequest = Readonly<z.output<typeof schema>>;

export function createVirtualizedPixelOffsetRequest(
  key: string,
  offsetPx: number,
): VirtualizedPixelOffsetRequest {
  return schema.parse({ key, offsetPx });
}

export function requireVirtualizedPixelOffsetRequest(
  request: VirtualizedPixelOffsetRequest | undefined,
): VirtualizedPixelOffsetRequest | undefined {
  return request === undefined ? undefined : schema.parse(request);
}
