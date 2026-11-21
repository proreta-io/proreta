import { useState } from "react";
import { ApiError } from "../api/client";
import type { InvitationExpiry, InvitationOptions } from "../api/endpoints";
import { useAuth } from "../auth/AuthProvider";
import { useEmailEnabled } from "../lib/oauth";
import { Checkbox, Field, Input, Select } from "./ui";

const EXPIRY_LABELS: Record<InvitationExpiry, string> = {
  "1h": "1 hour",
  "1d": "1 day",
  "7d": "7 days",
  "30d": "30 days",
};

/** What the page tells after a link was created: the choices made, not the address. */
export interface LinkChoices {
  bound: boolean;
  expiry: string;
}

/** The sentence under a new link: who can use it, and until when. */
export function linkNote(who: string, choices: LinkChoices): string {
  const until = `until it is used, revoked or ${choices.expiry} pass`;
  return choices.bound
    ? `Only the owner of the address it is for can use this link as ${who}, ${until}. With Google or GitHub the provider's verified address must match; with a password the address they type must match, which proves nothing about the mailbox, so keep the link private.`
    : `Anyone with this link can join as ${who} ${until}, whatever email address they use.`;
}

/**
 * The state and fields every invitation dialog shares: the optional address with two
 * independent choices (mail the link, bind it to the address) and how long the link works.
 */
export function useInvitationOptions() {
  const auth = useAuth();
  const emailEnabled = useEmailEnabled();
  const [email, setEmail] = useState("");
  // Until the person touches the box it follows the platform admin's default.
  const [sendChoice, setSendChoice] = useState<boolean | undefined>();
  const [bind, setBind] = useState(false);
  const [expiresIn, setExpiresIn] = useState<InvitationExpiry>("7d");
  const [emailError, setEmailError] = useState<string | undefined>();
  const address = email.trim();
  const send = emailEnabled && (sendChoice ?? auth.session?.emailInvitations === true);

  const request = (): InvitationOptions => ({
    expiresIn,
    ...(address ? { email: address, sendEmail: send } : {}),
    ...(bind ? { bindToEmail: true } : {}),
  });

  return {
    address,
    mails: send && address !== "",
    request,
    choices: (): LinkChoices => ({
      bound: bind && address !== "",
      expiry: EXPIRY_LABELS[expiresIn],
    }),
    reset: () => {
      setEmail("");
      setSendChoice(undefined);
      setBind(false);
      setExpiresIn("7d");
      setEmailError(undefined);
    },
    clearError: () => setEmailError(undefined),
    /** Shows a field problem of the address or the binding; false when it is something else. */
    fail: (err: unknown): boolean => {
      const fields = err instanceof ApiError ? err.fieldErrorMap() : {};
      const message = fields.email ?? fields.bindToEmail;
      if (message) setEmailError(message);
      return Boolean(message);
    },
    fields: (
      <>
        <Field label="How long the link works">
          {(c) => (
            <Select
              {...c}
              value={expiresIn}
              onChange={(e) => setExpiresIn(e.target.value as InvitationExpiry)}
            >
              {(Object.keys(EXPIRY_LABELS) as InvitationExpiry[]).map((v) => (
                <option key={v} value={v}>
                  {EXPIRY_LABELS[v]}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field
          label="Email address (optional)"
          hint="Where to send the link, and the address the link can be tied to."
          error={emailError}
        >
          {(c) => (
            <Input
              {...c}
              type="email"
              autoComplete="off"
              maxLength={254}
              value={email}
              onChange={(e) => {
                setEmail(e.target.value);
                setEmailError(undefined);
              }}
            />
          )}
        </Field>
        {emailEnabled && (
          <Checkbox
            label="Send the link by email"
            description="Proreta emails the link to this address when you create it."
            checked={send}
            onChange={(e) => setSendChoice(e.target.checked)}
          />
        )}
        <Checkbox
          label="Only this address can use the link"
          description="Someone who signs in with Google or GitHub needs this verified address. With a password it only has to match what they type."
          checked={bind}
          onChange={(e) => setBind(e.target.checked)}
        />
      </>
    ),
  };
}
