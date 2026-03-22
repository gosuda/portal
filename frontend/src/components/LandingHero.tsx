import { TunnelCommandForm } from "@/components/TunnelCommandForm";

export function LandingHero() {
  return (
    <section
      aria-labelledby="landing-title"
      className="relative overflow-hidden px-0 pb-16 pt-24"
    >
      <a
        href="#live-servers"
        className="sr-only focus:not-sr-only focus:absolute focus:left-6 focus:top-6 focus:z-20 focus:rounded-full focus:bg-background focus:px-4 focus:py-2 focus:text-sm focus:font-medium focus:text-foreground"
      >
        Skip to live servers
      </a>

      <div className="relative mx-auto max-w-4xl text-center">
        <h1
          id="landing-title"
          className="text-5xl font-extrabold leading-[1.1] tracking-tight text-foreground sm:text-6xl lg:text-7xl"
        >
          Expose Local Apps to the{" "}
          <br />
          <span
            className="bg-clip-text text-transparent"
            style={{
              backgroundImage:
                "linear-gradient(90deg, var(--hero-gradient-start) 0%, var(--hero-gradient-mid) 46%, var(--hero-gradient-end) 100%)",
            }}
          >
            Public Internet
          </span>
        </h1>
        <p className="mx-auto mt-6 max-w-2xl text-lg text-text-muted">
          Secure, lightning-fast tunnels with zero configuration. Share your work
          globally in seconds using our open-source relay network.
        </p>
      </div>

      <div className="relative mx-auto mt-12 max-w-[560px] overflow-hidden rounded-[1.75rem] border shadow-[0_18px_42px_oklch(0%_0_0_/_0.06)]">
        <div
          className="overflow-hidden rounded-[calc(1.75rem-1px)] border border-border/10"
          style={{
            background: "var(--hero-terminal-bg)",
            borderColor: "var(--hero-terminal-border)",
            color: "var(--hero-terminal-foreground)",
            boxShadow: "0 30px 72px var(--hero-terminal-shadow)",
          }}
        >
          <TunnelCommandForm theme="terminal" mode="hero" />
        </div>
      </div>
    </section>
  );
}
