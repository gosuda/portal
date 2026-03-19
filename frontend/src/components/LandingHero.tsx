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
        <div
          className="absolute inset-0 opacity-45 [background-size:14px_14px] [mask-image:linear-gradient(to_bottom,white,transparent_82%)]"
          style={{
            backgroundImage:
              "radial-gradient(var(--hero-grid-dot) 0.8px, transparent 0.8px)",
          }}
        />
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
          <span
            className="mt-2 block bg-clip-text text-transparent"
            style={{
              backgroundImage:
                "linear-gradient(90deg, var(--hero-gradient-start) 0%, var(--hero-gradient-mid) 46%, var(--hero-gradient-end) 100%)",
            }}
          >
            To The Public Internet
          </span>
        </h1>
        <p className="mx-auto mt-5 max-w-2xl text-lg leading-8 text-text-muted">
          Portal turns localhost into a public HTTPS URL. No port forwarding,
          NAT setup, or DNS configuration.
        </p>
      </div>

      <div
        className="relative mx-auto mt-10 max-w-[560px] rounded-[1.75rem] border p-5 sm:p-6"
        style={{
          background: "var(--hero-terminal-bg)",
          borderColor: "var(--hero-terminal-border)",
          color: "var(--hero-terminal-foreground)",
          boxShadow: "0 30px 72px var(--hero-terminal-shadow)",
        }}
      >
        <div className="mb-5 flex items-center gap-3">
          <Terminal
            className="h-5 w-5"
            style={{ color: "var(--hero-terminal-accent)" }}
          />
          <h2
            id="tunnel-preview"
            className="text-2xl font-bold tracking-tight"
          >
            Run this command
          </h2>
        </div>

        <TunnelCommandForm theme="terminal" mode="hero" />
      </div>
    </section>
  );
}
