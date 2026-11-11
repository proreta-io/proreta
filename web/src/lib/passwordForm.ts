import { codePoints, PASSWORD_MAX, PASSWORD_MIN } from "./registerForm";

export interface PasswordChangeValues {
  current: string;
  next: string;
  confirm: string;
}

export type PasswordChangeField = keyof PasswordChangeValues;
export type PasswordChangeErrors = Partial<Record<PasswordChangeField, string>>;

/** Client-side mirror of the server's rules for a new password (the same as registration's). */
export function validatePasswordChange(
  values: PasswordChangeValues,
  login: string,
): PasswordChangeErrors {
  const errors: PasswordChangeErrors = {};
  if (values.current === "") errors.current = "Enter your current password";
  const length = codePoints(values.next);
  if (length < PASSWORD_MIN) {
    errors.next = `Use at least ${PASSWORD_MIN} characters`;
  } else if (length > PASSWORD_MAX) {
    errors.next = `Use at most ${PASSWORD_MAX} characters`;
  } else if (values.next.toLowerCase() === login.trim().toLowerCase()) {
    errors.next = "Do not use your login as the password";
  } else if (values.next === values.current) {
    errors.next = "Choose a password different from the current one";
  }
  if (!errors.next && values.confirm !== values.next) {
    errors.confirm = "The passwords do not match";
  }
  return errors;
}
