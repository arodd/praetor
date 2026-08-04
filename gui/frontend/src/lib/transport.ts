import type {
  AppConfig,
  CommandHistoryUpdate,
  CredentialStoreStatus,
  InitState,
  WireEvent,
} from "./types";

export interface SystemUpdate {
  type:
    | "config"
    | "modes"
    | "accounts"
    | "operation"
    | "auth-expired"
    | "auth-restored"
    | "transport"
    | "command-history";
  config?: AppConfig;
  revision?: number;
  modeNames?: string[];
  accounts?: string[];
  credentialStore?: CredentialStoreStatus;
  result?: { operation: string; ok: boolean; message?: string };
  transportState?: "connecting" | "connected" | "reconnecting";
  commandHistory?: CommandHistoryUpdate;
}

export interface TransportHandlers {
  events: (batch: WireEvent[]) => void;
  snapshot?: (batch: WireEvent[]) => void;
  system?: (update: SystemUpdate) => void;
}

export interface PraetorTransport {
  readonly kind: "wails" | "web";
  invoke<T>(method: string, fallback: T, ...args: any[]): Promise<T>;
  subscribe(handlers: TransportHandlers): () => void;
  start(): Promise<void>;
  webLogin(password: string): Promise<void>;
  webLogout(): Promise<void>;
  quit(): Promise<void>;
  showLocalNotification(title: string, message: string): void;
  requestNotificationPermission(): Promise<NotificationPermission | "unsupported">;
}

export class WebAuthRequiredError extends Error {
  constructor() {
    super("Web authentication required");
    this.name = "WebAuthRequiredError";
  }
}

export interface WebBootstrap extends InitState {
  csrf: string;
  protocol: number;
  serverId: string;
  configRevision: number;
}
