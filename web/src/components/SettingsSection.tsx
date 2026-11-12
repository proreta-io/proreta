import type { ReactNode } from "react";

export default function SettingsSection({
  id,
  title,
  description,
  children,
}: {
  id: string;
  title: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <section
      aria-labelledby={`${id}-heading`}
      className="grid gap-4 border-t border-border pt-6 pb-7 sm:grid-cols-[220px_minmax(0,1fr)] sm:gap-12 sm:pt-8 sm:pb-10"
    >
      <div className="flex flex-col gap-1.5">
        <h2 id={`${id}-heading`} tabIndex={-1} className="text-lead font-semibold">
          {title}
        </h2>
        {description && <p className="text-[13px] text-muted-foreground">{description}</p>}
      </div>
      <div className="flex min-w-0 max-w-[560px] flex-col">{children}</div>
    </section>
  );
}
