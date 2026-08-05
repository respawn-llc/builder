export type TranscriptDisclosureIconTone = "neutral" | "warning" | "error" | "success";

const iconToneClassNames: Readonly<Record<TranscriptDisclosureIconTone, string>> = {
  neutral: "transcript-disclosure-icon--neutral",
  warning: "transcript-disclosure-icon--warning",
  error: "transcript-disclosure-icon--error",
  success: "transcript-disclosure-icon--success",
};

export function transcriptDisclosureIconToneClassName(iconTone: TranscriptDisclosureIconTone): string {
  return iconToneClassNames[iconTone];
}
