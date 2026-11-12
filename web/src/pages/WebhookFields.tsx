import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { ApiError } from "../api/client";
import { previewChannelTemplate } from "../api/endpoints";
import { Button, Field, Segmented, Textarea } from "../components/ui";
import type { ChannelFormErrors, ChannelFormValues } from "../lib/channels";
import {
  defaultTemplates,
  insertAt,
  isDefaultTemplate,
  placeholders,
  validateTemplate,
  type WebhookContentType,
} from "../lib/webhookTemplate";

const PREVIEW_DELAY_MS = 400;

function useDebounced<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delay);
    return () => clearTimeout(timer);
  }, [value, delay]);
  return debounced;
}

/** Content type, message template with its placeholders, and a live preview of the rendered sample. */
export default function WebhookFields({
  values,
  errors,
  onChange,
}: {
  values: Pick<ChannelFormValues, "contentType" | "bodyTemplate">;
  errors: ChannelFormErrors;
  onChange: (patch: Partial<ChannelFormValues>) => void;
}) {
  const textarea = useRef<HTMLTextAreaElement>(null);
  const { contentType, bodyTemplate } = values;
  const debounced = useDebounced({ contentType, bodyTemplate }, PREVIEW_DELAY_MS);
  const clientError = validateTemplate(debounced.bodyTemplate);
  const preview = useQuery({
    queryKey: ["channelTemplatePreview", debounced.contentType, debounced.bodyTemplate],
    queryFn: () => previewChannelTemplate(debounced),
    enabled: !clientError,
    retry: false,
    staleTime: Infinity,
  });
  const previewError =
    clientError ??
    (preview.error instanceof ApiError
      ? (preview.error.fieldErrorMap().bodyTemplate ?? preview.error.message)
      : preview.error
        ? "The preview is not available."
        : undefined);

  function chooseType(next: WebhookContentType) {
    // An untouched default follows the content type.
    onChange({
      contentType: next,
      bodyTemplate: isDefaultTemplate(bodyTemplate) ? defaultTemplates[next] : bodyTemplate,
    });
  }

  function insert(name: string) {
    const el = textarea.current;
    const start = el?.selectionStart ?? bodyTemplate.length;
    const end = el?.selectionEnd ?? bodyTemplate.length;
    const next = insertAt(bodyTemplate, start, end, `{{${name}}}`);
    onChange({ bodyTemplate: next.value });
    requestAnimationFrame(() => {
      el?.focus();
      el?.setSelectionRange(next.caret, next.caret);
    });
  }

  return (
    <>
      <div className="flex flex-col gap-1.5">
        <Segmented
          label="Content type"
          name="contentType"
          value={contentType}
          onChange={chooseType}
          options={[
            { value: "json", label: "JSON" },
            { value: "text", label: "Plain text" },
          ]}
        />
        <span className="text-caption text-muted-foreground">
          JSON escapes every placeholder value, so a name with quotes cannot break the document.
          Plain text inserts values as they are.
        </span>
      </div>

      <Field
        label="Message template"
        hint="Sent as the body of a POST. Write placeholders as {{name}}."
        error={errors.bodyTemplate}
      >
        {(c) => (
          <Textarea
            {...c}
            ref={textarea}
            name="bodyTemplate"
            rows={9}
            spellCheck={false}
            autoCapitalize="off"
            className="font-mono text-caption"
            value={bodyTemplate}
            onChange={(e) => onChange({ bodyTemplate: e.target.value })}
          />
        )}
      </Field>

      <div className="flex flex-col gap-1.5">
        <span className="text-caption font-medium">Placeholders</span>
        <div className="flex flex-wrap gap-1" role="group" aria-label="Insert a placeholder">
          {placeholders.map((p) => (
            <Button
              key={p.name}
              variant="ghost"
              size="small"
              title={p.help}
              className="font-mono"
              onClick={() => insert(p.name)}
            >
              {`{{${p.name}}}`}
            </Button>
          ))}
        </div>
      </div>

      <div className="flex flex-col gap-1.5" aria-live="polite">
        <span className="text-caption font-medium">Preview with sample data</span>
        {previewError ? (
          <span role="alert" className="text-caption text-down">
            {previewError}
          </span>
        ) : (
          <pre
            data-testid="template-preview"
            className="m-0 max-h-60 overflow-auto border border-border bg-muted p-2.5 font-mono text-caption break-words whitespace-pre-wrap"
          >
            {preview.data?.body ?? "…"}
          </pre>
        )}
      </div>
    </>
  );
}
