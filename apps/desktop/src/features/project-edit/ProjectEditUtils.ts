export function projectNameErrors(value: string, t: (key: string) => string): readonly string[] {
  const errors: string[] = [];
  const visibleLength = value.trim().length;
  if (visibleLength < 1 || visibleLength > 80) {
    errors.push(t("form.projectNameLength"));
  }
  if (value !== value.trim()) {
    errors.push(t("form.noEdgeWhitespace"));
  }
  if (hasLineBreak(value)) {
    errors.push(t("form.singleLine"));
  }
  return errors;
}

export function projectKeyErrors(value: string, t: (key: string) => string): readonly string[] {
  const errors: string[] = [];
  if (value.length < 2 || value.length > 8) {
    errors.push(t("form.projectKeyLength"));
  }
  if (hasWhitespace(value)) {
    errors.push(t("form.noWhitespace"));
  }
  if (!isAsciiUppercaseLetter(value.at(0) ?? "")) {
    errors.push(t("form.projectKeyStartsWithLetter"));
  }
  if (!hasOnlyAsciiUppercaseLettersAndDigits(value)) {
    errors.push(t("form.projectKeySymbols"));
  }
  return errors;
}

function hasLineBreak(value: string): boolean {
  for (const char of value) {
    if (char === "\n" || char === "\r") {
      return true;
    }
  }
  return false;
}

function hasWhitespace(value: string): boolean {
  for (const char of value) {
    if (char.trim().length === 0) {
      return true;
    }
  }
  return false;
}

function hasOnlyAsciiUppercaseLettersAndDigits(value: string): boolean {
  for (const char of value) {
    if (!isAsciiUppercaseLetter(char) && !isAsciiDigit(char)) {
      return false;
    }
  }
  return true;
}

function isAsciiUppercaseLetter(value: string): boolean {
  if (value.length !== 1) {
    return false;
  }
  const code = value.charCodeAt(0);
  return code >= 65 && code <= 90;
}

function isAsciiDigit(value: string): boolean {
  if (value.length !== 1) {
    return false;
  }
  const code = value.charCodeAt(0);
  return code >= 48 && code <= 57;
}
