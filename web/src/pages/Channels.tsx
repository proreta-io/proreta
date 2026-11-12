import { NotificationChannels } from "./Settings";

/** Notification channels of the projects the person sees and the channels shared by all projects. */
export default function Channels() {
  return (
    <section className="flex max-w-[960px] flex-col gap-4">
      <div className="flex flex-col gap-2 pt-6 sm:pt-10">
        <h1 className="text-headline leading-[1.1] font-semibold tracking-[-0.02em] sm:text-display">
          Notification channels
        </h1>
        <p className="max-w-[560px] text-muted-foreground">
          Slack channels Proreta posts to through Incoming Webhooks, and webhooks that receive a
          message of your own template. Each channel chooses its events, an optional mention for
          down alerts and an optional reminder while an incident stays open.
        </p>
      </div>
      <div className="flex max-w-[560px] flex-col">
        <NotificationChannels />
      </div>
    </section>
  );
}
