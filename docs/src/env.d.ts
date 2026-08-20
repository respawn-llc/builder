/// <reference types="astro/client" />

declare module "virtual:starlight/components/*" {
  const component: import("astro/runtime/server/index.js").AstroComponentFactory;
  export default component;
}

declare module "virtual:starlight/docsearch-config" {
  const config: Parameters<typeof import("@docsearch/js").default>[0];
  export default config;
}

interface StarlightThemeProvider {
  applyEffectiveTheme(): "light" | "dark";
  getEffectiveTheme(): "light" | "dark";
  getStoredTheme(): "light" | "dark" | null;
  toggleTheme(): "light" | "dark";
}

interface Window {
  StarlightThemeProvider: StarlightThemeProvider;
  __kentDocsSmoothHashScrollInstalled?: boolean;
}

interface Navigator {
  userAgentData?: {
    platform?: string;
    getHighEntropyValues(hints: string[]): Promise<{ architecture?: string }>;
  };
}
