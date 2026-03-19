import { Terminal } from "lucide-react";
import { TunnelCommandForm } from "@/components/TunnelCommandForm";

export function LandingHero() {
  return (
    <section
      aria-labelledby="landing-title"
      className="px-0 py-8 sm:py-10 lg:py-12"
    >
      <a
        href="#live-servers"
        className="sr-only focus:not-sr-only focus:absolute focus:left-6 focus:top-6 focus:z-20 focus:rounded-full focus:bg-background focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:text-foreground"
      >
        Skip to live servers
      </a>

      <div className="mx-auto max-w-4xl text-center">
        <h1
          id="landing-title"
          className="text-4xl font-extrabold tracking-tight text-foreground sm:text-5xl lg:text-6xl"
        >
          Expose localhost instantly
        </h1>
        <p className="mx-auto mt-5 max-w-2xl text-lg leading-8 text-text-muted">
          No signup. No config. Public URL in seconds.
        </p>
      </div>

      <div className="mx-auto mt-10 max-w-[560px] rounded-[1.75rem] border border-white/10 bg-slate-950 p-5 text-white shadow-[0_30px_72px_rgba(15,23,42,0.22)] sm:p-6">
        <div className="mb-5 flex items-center gap-3">
          <Terminal className="h-5 w-5 text-green-status" />
          <h2
            id="tunnel-preview"
            className="text-2xl font-bold tracking-tight text-white"
          >
            Run this command
          </h2>
        </div>

        <TunnelCommandForm theme="terminal" mode="hero" />
      </div>
    </section>
  );
}
