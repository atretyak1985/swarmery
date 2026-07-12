/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** '1' enables offline mock mode (fixture data + fake WS). */
  readonly VITE_MOCK?: string;
}
