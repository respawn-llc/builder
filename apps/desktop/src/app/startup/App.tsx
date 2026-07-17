import type { AppServices } from "@/app-facade";
import { AppRoot } from "../AppRoot";

export type AppProps = Readonly<{
  services: AppServices;
}>;

export function App({ services }: AppProps) {
  return <AppRoot services={services} />;
}
