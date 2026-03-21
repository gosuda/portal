import { Terminal } from "lucide-react";
import { TunnelCommandForm } from "@/components/TunnelCommandForm";

const heroFeatures = [
  {
    title: "No setup. No port forwarding.",
    description:
      "Works instantly, even behind NAT and firewalls.",
  },
  {
    title: "End-to-end TLS",
    description:
      "Traffic is routed via SNI with keyless TLS, while TLS still terminates on your app.",
  },
  {
    title: "Permissionless hosting",
    description:
      "Attach to arbitrary relays - no accounts, no approval, no trust required.",
  },
  {
    title: "UDP support",
    description:
      "Expose web apps and arbitrary protocols through the same tunnel.",
  },
  {
    title: "One command. Done.",
    description:
      "Install and expose your app in a single copy-paste.",
  },
] as const;

export function LandingHero() {
  return (
    <section
      aria-labelledby="landing-title"
      className="relative pt-10 sm:pt-12 lg:pt-16"
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

      <div className="relative mx-auto max-w-4xl px-2 text-center sm:px-4">
        <h1
          id="landing-title"
          className="text-4xl font-extrabold tracking-tight text-foreground sm:text-5xl lg:text-7xl"
          style={{ lineHeight: 0.96 }}
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
        className="relative mx-auto mt-10 w-full max-w-[520px] rounded-[1.75rem] border px-4 py-5 sm:px-5 sm:py-6"
        style={{
          background: "var(--hero-terminal-bg)",
          borderColor: "var(--hero-terminal-border)",
          color: "var(--hero-terminal-foreground)",
          boxShadow: "0 30px 72px var(--hero-terminal-shadow)",
        }}
      >
        <div className="mb-5 flex min-w-0 items-center gap-3">
          <Terminal
            className="h-5 w-5 shrink-0"
            style={{ color: "var(--hero-terminal-accent)" }}
          />
          <h2
            id="tunnel-preview"
            className="min-w-0 text-xl font-bold tracking-tight sm:text-2xl"
          >
            Run this command
          </h2>
        </div>

        <TunnelCommandForm theme="terminal" mode="hero" />
      </div>

      <div className="relative mt-18 -mx-4 w-auto sm:mt-20 sm:-mx-6 md:-mx-8">
        <div className="overflow-hidden border-t border-border/80 bg-border/70">
          <div className="grid gap-px sm:grid-cols-2 lg:grid-cols-3">
            <div className="flex min-h-[184px] bg-background/88 p-6 text-left sm:min-h-[196px] sm:p-7">
              <div className="space-y-2">
                <h2 className="whitespace-nowrap text-[1.2rem] font-semibold tracking-tight text-foreground sm:text-[1.32rem] sm:leading-none">
                  Make localhost public
                </h2>
                <p className="max-w-[28ch] text-[0.95rem] leading-6 text-text-muted">
                  Turn any local app into a shareable HTTPS URL in seconds.
                </p>
              </div>
            </div>

            {heroFeatures.map(({ title, description }) => (
              <article
                key={title}
                className="flex min-h-[184px] bg-background/88 p-6 text-left transition-colors duration-200 hover:bg-background/92 sm:min-h-[196px] sm:p-7"
              >
                <div className="space-y-2">
                  <h3 className="whitespace-nowrap text-[1.2rem] font-semibold tracking-tight text-foreground sm:text-[1.32rem] sm:leading-none">
                    {title}
                  </h3>
                  <p className="max-w-[28ch] text-[0.95rem] leading-6 text-text-muted">
                    {description}
                  </p>
                </div>
              </article>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}
