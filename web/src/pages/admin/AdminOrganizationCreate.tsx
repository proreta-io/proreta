import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router-dom";
import { ApiError, errorMessage } from "../../api/client";
import {
  createAdminOrganization,
  listAdminPlans,
  queryKeys,
  type AdminOrganization,
} from "../../api/endpoints";
import {
  Alert,
  Button,
  ConfirmDialog,
  CopyField,
  Field,
  Input,
  Select,
  buttonClass,
} from "../../components/ui";

/** What the platform admin must hand over after creating: kept in component state only. */
interface Created {
  organization: AdminOrganization;
  ownerLogin?: string;
  inviteUrl?: string;
}

function absoluteLink(url: string) {
  return url.startsWith("/") ? `${globalThis.location.origin}${url}` : url;
}

/** "New organization": a dialog, then the owner outcome (a link is shown once). */
export default function AdminOrganizationCreate() {
  const queryClient = useQueryClient();
  const plans = useQuery({ queryKey: queryKeys.adminPlans, queryFn: listAdminPlans });
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [planKey, setPlanKey] = useState("internal");
  const [ownerEmail, setOwnerEmail] = useState("");
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | undefined>();
  const [created, setCreated] = useState<Created | null>(null);

  const create = useMutation({
    mutationFn: () =>
      createAdminOrganization({
        name: name.trim(),
        ownerEmail: ownerEmail.trim(),
        planKey,
        ...(slug.trim() ? { slug: slug.trim() } : {}),
      }),
    // The answer may hold a secret link: it must not stay in the query cache.
    gcTime: 0,
    onSuccess: (res) => {
      setOpen(false);
      setCreated({
        organization: res.organization,
        ownerLogin: res.member?.login,
        inviteUrl: res.inviteUrl ? absoluteLink(res.inviteUrl) : undefined,
      });
      setName("");
      setSlug("");
      setPlanKey("internal");
      setOwnerEmail("");
      create.reset();
      void queryClient.invalidateQueries({ queryKey: ["admin"] });
    },
    onError: (err) => {
      const fields = err instanceof ApiError ? err.fieldErrorMap() : {};
      setFieldErrors(fields);
      setError(Object.keys(fields).length > 0 ? undefined : errorMessage(err));
    },
  });

  return (
    <>
      <Button
        variant="primary"
        size="small"
        onClick={() => {
          setError(undefined);
          setFieldErrors({});
          setOpen(true);
        }}
      >
        New organization
      </Button>

      {created && (
        <section
          aria-labelledby="admin-created-heading"
          className="flex w-full flex-col gap-4 border border-border p-5"
        >
          <h2 id="admin-created-heading" className="text-lead font-semibold">
            Organization {created.organization.name} created
          </h2>
          {created.ownerLogin && (
            <p className="text-muted-foreground">
              <strong>{created.ownerLogin}</strong> is its owner and can switch to it now.
            </p>
          )}
          {created.inviteUrl && (
            <>
              <p className="text-muted-foreground">
                There is no account with that email yet. Send this link to the person: they join as
                a maintainer (a link never makes an owner). Promote them to owner under Members
                after they joined.
              </p>
              <CopyField label="Invitation link" value={created.inviteUrl} copyLabel="Copy link" />
              <p className="text-muted-foreground">
                It works once and for 7 days. It is shown only now; Proreta keeps only a hash of it.
              </p>
            </>
          )}
          <div className="flex gap-3">
            <Link
              to={`/admin/organizations/${created.organization.id}`}
              className={buttonClass("secondary", "small")}
            >
              Open organization
            </Link>
            <Button variant="secondary" size="small" onClick={() => setCreated(null)}>
              {created.inviteUrl ? "I sent it, hide the link" : "Close"}
            </Button>
          </div>
        </section>
      )}

      {open && (
        <ConfirmDialog
          title="New organization"
          confirmLabel="Create organization"
          confirmVariant="primary"
          busy={create.isPending}
          onCancel={() => setOpen(false)}
          onConfirm={() => {
            setError(undefined);
            setFieldErrors({});
            create.mutate();
          }}
        >
          <div className="flex flex-col gap-4">
            {error && <Alert>{error}</Alert>}
            <Field label="Name" error={fieldErrors.name}>
              {(c) => (
                <Input
                  {...c}
                  maxLength={200}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                />
              )}
            </Field>
            <Field
              label="Slug (optional)"
              hint="Lowercase letters, digits and hyphens. A random one is chosen when empty."
              error={fieldErrors.slug}
            >
              {(c) => (
                <Input
                  {...c}
                  maxLength={63}
                  autoComplete="off"
                  value={slug}
                  onChange={(e) => setSlug(e.target.value)}
                />
              )}
            </Field>
            <Field label="Plan" error={fieldErrors.planKey}>
              {(c) => (
                <Select {...c} value={planKey} onChange={(e) => setPlanKey(e.target.value)}>
                  {(plans.data?.items ?? [{ key: "internal", name: "Internal" }]).map((p) => (
                    <option key={p.key} value={p.key}>
                      {p.name}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            <Field
              label="Owner's email"
              hint="An existing account becomes the owner. Without one you get an invitation link to send."
              error={fieldErrors.ownerEmail}
            >
              {(c) => (
                <Input
                  {...c}
                  type="email"
                  autoComplete="off"
                  maxLength={254}
                  value={ownerEmail}
                  onChange={(e) => setOwnerEmail(e.target.value)}
                />
              )}
            </Field>
          </div>
        </ConfirmDialog>
      )}
    </>
  );
}
