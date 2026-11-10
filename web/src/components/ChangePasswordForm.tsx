import { useState, type FormEvent } from "react";
import {
  ApiError,
  errorMessage,
  NO_PASSWORD_PROBLEM_TYPE,
  PASSWORD_LOGIN_DISABLED_PROBLEM_TYPE,
} from "../api/client";
import { useAuth } from "../auth/AuthProvider";
import { formatDuration } from "../lib/format";
import {
  validatePasswordChange,
  type PasswordChangeErrors,
  type PasswordChangeField,
  type PasswordChangeValues,
} from "../lib/passwordForm";
import { PASSWORD_MAX, PASSWORD_MIN } from "../lib/registerForm";
import { ShowPassword } from "./AuthScreen";
import { useToast } from "./Toasts";
import { Alert, Button, Field, Input } from "./ui";

const EMPTY: PasswordChangeValues = { current: "", next: "", confirm: "" };

/** Maps a failed change to field errors and a general message. */
export function passwordChangeErrors(err: unknown): {
  fields: PasswordChangeErrors;
  general?: string;
} {
  if (err instanceof ApiError) {
    if (err.type === NO_PASSWORD_PROBLEM_TYPE) {
      return { fields: {}, general: "You have no password; you sign in with a provider." };
    }
    if (err.type === PASSWORD_LOGIN_DISABLED_PROBLEM_TYPE) {
      return { fields: {}, general: "Password sign-in is turned off on this Proreta." };
    }
    if (err.status === 429) {
      return {
        fields: {},
        general: err.retryAfter
          ? `Too many attempts. Try again in ${formatDuration(err.retryAfter)}.`
          : "Too many attempts. Try again later.",
      };
    }
    if (err.status === 422 || err.status === 400) {
      const map = err.fieldErrorMap();
      const fields: PasswordChangeErrors = {};
      if (map.currentPassword) fields.current = `Current password ${map.currentPassword}`;
      if (map.newPassword) fields.next = `New password ${map.newPassword}`;
      if (fields.current || fields.next) return { fields };
    }
  }
  return { fields: {}, general: errorMessage(err) };
}

/**
 * Change password for the signed-in user. A success replaces the session (the other devices are
 * signed out), so the page stays as it is and says what happened.
 */
export default function ChangePasswordForm() {
  const auth = useAuth();
  const toast = useToast();
  const [values, setValues] = useState<PasswordChangeValues>(EMPTY);
  const [errors, setErrors] = useState<PasswordChangeErrors>({});
  const [general, setGeneral] = useState<string | undefined>();
  const [submitting, setSubmitting] = useState(false);
  const [shown, setShown] = useState(false);

  function set(field: PasswordChangeField, value: string) {
    setValues((v) => ({ ...v, [field]: value }));
    setErrors((e) => (e[field] ? { ...e, [field]: undefined } : e));
  }

  function focusFirst(form: HTMLFormElement, found: PasswordChangeErrors) {
    const first = (["current", "next", "confirm"] as const).find((f) => found[f]);
    if (first) form.querySelector<HTMLInputElement>(`input[name=${first}]`)?.focus();
  }

  async function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const found = validatePasswordChange(values, auth.session?.user.login ?? "");
    setErrors(found);
    setGeneral(undefined);
    if (Object.values(found).some(Boolean)) {
      focusFirst(form, found);
      return;
    }
    setSubmitting(true);
    try {
      await auth.changePassword(values.current, values.next);
      setValues(EMPTY);
      setShown(false);
      toast("Password changed. Your other devices were signed out.");
    } catch (err) {
      const mapped = passwordChangeErrors(err);
      setErrors(mapped.fields);
      setGeneral(mapped.general);
      focusFirst(form, mapped.fields);
    } finally {
      setSubmitting(false);
    }
  }

  const hasErrors = Object.values(errors).some(Boolean);
  const confirmMatches = values.confirm !== "" && values.confirm === values.next && !errors.confirm;

  return (
    <form onSubmit={onSubmit} noValidate className="flex max-w-[420px] flex-col gap-[18px]">
      {(general || hasErrors) && <Alert>{general ?? "Check the highlighted fields."}</Alert>}
      <Field label="Current password" error={errors.current}>
        {(c) => (
          <Input
            {...c}
            name="current"
            type={shown ? "text" : "password"}
            autoComplete="current-password"
            required
            readOnly={submitting}
            value={values.current}
            onChange={(e) => set("current", e.target.value)}
            action={
              <ShowPassword
                shown={shown}
                plural
                controls={c.id}
                onToggle={() => setShown((s) => !s)}
              />
            }
          />
        )}
      </Field>
      <Field
        label="New password"
        hint={`${PASSWORD_MIN} to ${PASSWORD_MAX} characters. A passphrase works well.`}
        error={errors.next}
      >
        {(c) => (
          <Input
            {...c}
            name="next"
            type={shown ? "text" : "password"}
            autoComplete="new-password"
            required
            readOnly={submitting}
            value={values.next}
            onChange={(e) => set("next", e.target.value)}
          />
        )}
      </Field>
      <Field
        label="Confirm new password"
        error={errors.confirm}
        hint={confirmMatches ? "Matches" : undefined}
      >
        {(c) => (
          <Input
            {...c}
            name="confirm"
            type={shown ? "text" : "password"}
            autoComplete="new-password"
            required
            readOnly={submitting}
            value={values.confirm}
            onChange={(e) => set("confirm", e.target.value)}
          />
        )}
      </Field>
      <Button type="submit" busy={submitting} className="self-start">
        {submitting ? "Changing password…" : "Change password"}
      </Button>
    </form>
  );
}
