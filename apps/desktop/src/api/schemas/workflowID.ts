import { z } from "zod";

export const workflowIDSchema = z.uuidv4().refine((value) => value === value.toLowerCase(), {
  message: "Workflow IDs must use lower-case canonical UUIDv4 text",
});
