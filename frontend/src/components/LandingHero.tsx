import { Terminal } from "lucide-react";
import { TunnelCommandForm } from "@/components/TunnelCommandForm";

export function LandingHero() {
  return (
    <section
      aria-labelledby="landing-title"
      className="relative overflow-hidden px-0 py-10 sm:py-12 lg:py-16"
    >
      <div
        aria-hidden="true"
        className="pointer-events-none absolute inset-0"
      >
        <div className="absolute inset-0 opacity-45 [background-image:radial-gradient(rgba(99,102,241,0.12)_0.8px,transparent_0.8px)] [background-size:14px_14px] [mask-image:linear-gradient(to_bottom,white,transparent_82%)]" />
      </div>

      <a
        href="#live-servers"
        className="sr-only focus:not-sr-only focus:absolute focus:left-6 focus:top-6 focus:z-20 focus:rounded-full focus:bg-background focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:text-foreground"
      >
        Skip to live servers
      </a>

      <div className="relative mx-auto max-w-4xl text-center">
        <h1
          id="landing-title"
          className="text-5xl font-extrabold tracking-tight text-foreground sm:text-6xl lg:text-7xl"
        >
          <span className="block">Expose Local Apps</span>
          <span className="mt-2 block bg-[linear-gradient(90deg,#7c6cff_0%,#6c8fff_46%,#57c6ff_100%)] bg-clip-text text-transparent">
            To The Public Internet
          </span>
        </h1>
        <p className="mx-auto mt-5 max-w-2xl text-lg leading-8 text-text-muted">
          Portal turns localhost into a public HTTPS URL. No port forwarding,
          NAT setup, or DNS configuration.
        </p>
      </div>

      <div className="relative mx-auto mt-10 max-w-[560px] rounded-[1.75rem] border border-white/10 bg-slate-950 p-5 text-white shadow-[0_30px_72px_rgba(15,23,42,0.22)] sm:p-6">
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
