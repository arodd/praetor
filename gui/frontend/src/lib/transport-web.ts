import { tick } from "svelte";
import type {
  AppConfig,
  AccountState,
  ConnectResult,
  CommandHistoryUpdate,
  CredentialStoreStatus,
  DesktopNotificationsConfig,
  InitState,
  KudosConfig,
  TypedCommandResult,
  WireEvent,
} from "./types";
import type {
  PraetorTransport,
  SystemUpdate,
  TransportHandlers,
  WebBootstrap,
} from "./transport";
import { WebAuthRequiredError } from "./transport";

interface WebEnvelope {
  type: "snapshot" | "events" | "config" | "modes" | "accounts" | "operation" | "commandHistory";
  protocol: number;
  serverId: string;
  sequence?: number;
  fromSequence?: number;
  toSequence?: number;
  events?: WireEvent[];
  config?: AppConfig;
  revision?: number;
  modeNames?: string[];
  accounts?: string[];
  credentialStore?: CredentialStoreStatus;
  result?: { operation: string; ok: boolean; message?: string };
  commandHistory?: CommandHistoryUpdate;
}

export type WebEventStreamFailureCategory =
  | "json_parse_failure"
  | "protocol_validation_failure"
  | "sequence_gap"
  | "event_handler_failure"
  | "server_resynchronization"
  | "client_backlog_limit"
  | "network_close";

export interface WebEventStreamClientDiagnostics {
  parsedMessages: number;
  parsedBytes: number;
  maxParseMilliseconds: number;
  appliedEnvelopes: number;
  appliedEvents: number;
  maxApplicationMilliseconds: number;
  reconciliationSamples: number;
  maxReconciliationMilliseconds: number;
  outputPaneRenderSamples: number;
  maxOutputPaneRenderMilliseconds: number;
  mainThreadYields: number;
  maxYieldMilliseconds: number;
  reconnects: number;
  failures: Record<WebEventStreamFailureCategory, number>;
  lastFailure?: {
    category: WebEventStreamFailureCategory;
    message: string;
  };
  lastClose?: {
    code: number;
    reason: string;
    category: WebEventStreamFailureCategory;
  };
}

interface QueuedWebEnvelope {
  socket: WebSocket;
  epoch: number;
  envelope: WebEnvelope;
  encodedBytes: number;
  eventCount: number;
}

class WebEnvelopeValidationError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "WebEnvelopeValidationError";
  }
}

class WebSequenceGapError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "WebSequenceGapError";
  }
}

class WebEventHandlerError extends Error {
  constructor(cause: unknown) {
    super(cause instanceof Error ? cause.message : String(cause));
    this.name = "WebEventHandlerError";
  }
}

class StaleWebEventStreamError extends Error {
  constructor() {
    super("Superseded Praetor event stream");
    this.name = "StaleWebEventStreamError";
  }
}

const WEB_EVENT_APPLY_CHUNK_SIZE = 100;
const WEB_INBOUND_MAX_EVENTS = 8192;
const WEB_INBOUND_MAX_BYTES = 16 << 20;
const UTF8_ENCODER = new TextEncoder();

function newEventStreamDiagnostics(): WebEventStreamClientDiagnostics {
  return {
    parsedMessages: 0,
    parsedBytes: 0,
    maxParseMilliseconds: 0,
    appliedEnvelopes: 0,
    appliedEvents: 0,
    maxApplicationMilliseconds: 0,
    reconciliationSamples: 0,
    maxReconciliationMilliseconds: 0,
    outputPaneRenderSamples: 0,
    maxOutputPaneRenderMilliseconds: 0,
    mainThreadYields: 0,
    maxYieldMilliseconds: 0,
    reconnects: 0,
    failures: {
      json_parse_failure: 0,
      protocol_validation_failure: 0,
      sequence_gap: 0,
      event_handler_failure: 0,
      server_resynchronization: 0,
      client_backlog_limit: 0,
      network_close: 0,
    },
  };
}

function monotonicNow(): number {
  return typeof performance !== "undefined" ? performance.now() : Date.now();
}

function envelopeEventCount(message: WebEnvelope): number {
  return Array.isArray(message?.events) ? message.events.length : 0;
}

interface ErrorResponse {
  error?: { code?: string; message?: string; requestId?: string };
}

interface SessionSignal {
  type: "praetor-session-changed";
  action: "login" | "logout";
  source: string;
  generation: string;
}

interface BootstrapRefresh {
  init: WebBootstrap;
  serverChanged: boolean;
}

const sessionChannelName = "praetor-web-session-v1";

class WebAPIError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "WebAPIError";
    this.status = status;
    this.code = code;
  }
}

export class WebTransport implements PraetorTransport {
  readonly kind = "web" as const;

  private csrf = "";
  private revision = 0;
  private serverId = "";
  private sequence = 0;
  private socket: WebSocket | null = null;
  private handlers = new Set<TransportHandlers>();
  private started = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempt = 0;
  private socketReady = false;
  private csrfRefresh: Promise<BootstrapRefresh> | null = null;
  private authExpired = false;
  private readonly sessionSource = randomSessionMarker();
  private sessionChannel: BroadcastChannel | null = null;
  private streamEpoch = 0;
  private inboundQueue: QueuedWebEnvelope[] = [];
  private inboundQueuedEvents = 0;
  private inboundQueuedBytes = 0;
  private inboundProcessing = false;
  private localClose: {
    epoch: number;
    category: WebEventStreamFailureCategory;
  } | null = null;
  private streamDiagnostics = newEventStreamDiagnostics();

  constructor() {
    if (
      typeof window !== "undefined" &&
      typeof BroadcastChannel !== "undefined"
    ) {
      try {
        this.sessionChannel = new BroadcastChannel(sessionChannelName);
        this.sessionChannel.onmessage = (event: MessageEvent<unknown>) => {
          const signal = parseSessionSignal(event.data);
          if (!signal || signal.source === this.sessionSource) return;
          void this.handleSessionSignal(signal);
        };
      } catch {
        // Some embedded/private browser contexts expose BroadcastChannel but
        // deny its construction. Typed CSRF recovery remains the fallback.
        this.sessionChannel = null;
      }
    }
  }

  eventStreamDiagnostics(): WebEventStreamClientDiagnostics {
    return {
      ...this.streamDiagnostics,
      failures: { ...this.streamDiagnostics.failures },
      lastFailure: this.streamDiagnostics.lastFailure
        ? { ...this.streamDiagnostics.lastFailure }
        : undefined,
      lastClose: this.streamDiagnostics.lastClose
        ? { ...this.streamDiagnostics.lastClose }
        : undefined,
    };
  }

  async invoke<T>(method: string, fallback: T, ...args: any[]): Promise<T> {
    switch (method) {
      case "GetInitState": {
        const init = await this.request<WebBootstrap>("GET", "/api/v1/bootstrap");
        this.installBootstrap(init);
        return init as T;
      }
      case "GetConfig": {
        const init = await this.request<WebBootstrap>("GET", "/api/v1/bootstrap");
        this.installBootstrap(init);
        return init.config as T;
      }
      case "ListAccounts": {
        return (await this.request<AccountState>("GET", "/api/v1/accounts")) as T;
      }
      case "ConnectNew":
        return (await this.request<ConnectResult>("POST", "/api/v1/game/connect", {
          username: args[0],
          password: args[1],
          store: args[2],
        })) as T;
      case "ConnectStored":
        await this.request("POST", "/api/v1/game/connect-stored", { username: args[0] });
        return undefined as T;
      case "SaveAccount":
        await this.request("PUT", `/api/v1/accounts/${encodeURIComponent(args[0])}`, { password: args[1] });
        return undefined as T;
      case "RemoveAccount":
        await this.request("DELETE", `/api/v1/accounts/${encodeURIComponent(args[0])}`);
        return undefined as T;
      case "Disconnect":
        await this.request("POST", "/api/v1/game/disconnect", {});
        return undefined as T;
      case "Send":
        await this.request("POST", "/api/v1/commands", { input: args[0] });
        return undefined as T;
      case "SubmitTypedCommand": {
        const result = await this.request<TypedCommandResult>(
          "POST",
          "/api/v1/typed-commands",
          { input: args[0], disposition: args[1], submissionId: args[2] },
        );
        this.emitSystem({ type: "command-history", commandHistory: result.history });
        return result as T;
      }
      case "ModeNames": {
        const data = await this.request<{ modeNames: string[] }>("GET", "/api/v1/modes");
        return (data.modeNames ?? []) as T;
      }
      case "CurrentMode": {
        const data = await this.request<{ currentMode: string }>("GET", "/api/v1/modes");
        return (data.currentMode ?? "") as T;
      }
      case "SetMode":
        await this.request("PUT", "/api/v1/mode", { name: args[0], args: args[1] });
        return undefined as T;
      case "ReloadScripts":
        await this.request("POST", "/api/v1/scripts/reload", {});
        return undefined as T;
      case "PickScriptDir":
        // Browser clients cannot open a native picker on the server host. The
        // web scripts editor accepts server-side paths as text instead.
        return fallback;
      case "PickSendFile":
      case "PickPlayFile":
        // /send and /play read local files through a native picker and have no
        // praetor-web endpoints yet. Both call sites catch and toast, so the
        // browser user gets an explanation instead of a silent cancel.
        throw new Error("/send and /play scripts are not available in the browser client yet.");
      case "StartFileSend":
      case "StartPlay":
        // Unreachable in the browser: starting either flow requires a
        // successful pick above. The explicit decision keeps the parity
        // contract honest.
        return fallback;
      case "AbortSend":
      case "PausePlay":
      case "ResumePlay":
      case "StopPlay":
      case "NextPlayStep":
      case "PlayActive":
      case "PlayStatus":
        // No file send or performance can exist in a browser session. These
        // report the idle state (false / inactive status) so Alt+X, the
        // pre-submit play gate, and the slash controls behave exactly like an
        // idle desktop instead of erroring.
        return fallback;
      case "RefreshGraphics":
        await this.request("POST", "/api/v1/graphics/refresh", {});
        return undefined as T;
      case "ClipboardGet":
        if (!navigator.clipboard?.readText) throw new Error("Browser clipboard read is unavailable; use the browser's Paste command.");
        return (await navigator.clipboard.readText()) as T;
      case "ClipboardSet":
        if (navigator.clipboard?.writeText) {
          await navigator.clipboard.writeText(args[0]);
        } else {
          this.copyFallback(args[0]);
        }
        return undefined as T;
      case "GetKudos":
        return (await this.request<KudosConfig>("GET", "/api/v1/kudos")) as T;
      case "SetKudos": {
        const result = await this.request<ConfigMutationResponse>("PUT", "/api/v1/kudos", {
          expectedRevision: this.revision,
          value: args[0],
        });
        this.acceptConfigMutation(result);
        return undefined as T;
      }
      case "AddKudosFavorite": {
        const result = await this.request<ConfigMutationResponse & { added: boolean }>(
          "POST",
          "/api/v1/kudos/favorites",
          { name: args[0] },
        );
        this.acceptConfigMutation(result);
        return result.added as T;
      }
      case "AddKudosQueue": {
        const result = await this.request<ConfigMutationResponse>(
          "POST",
          "/api/v1/kudos/queue",
          { name: args[0], message: args[1] },
        );
        this.acceptConfigMutation(result);
        return undefined as T;
      }
      case "GetPersistentData":
        return (await this.request("GET", "/api/v1/persistent")) as T;
      case "ExportPersistentData":
        return (await this.downloadPersistent(args[0])) as T;
      case "ClearPersistentData":
        await this.request("DELETE", "/api/v1/persistent", { keys: args[0] });
        return undefined as T;
      case "ListNotes":
        return (await this.request("GET", "/api/v1/notes")) as T;
      case "GetNote":
        return (await this.request("GET", `/api/v1/notes/${encodeURIComponent(args[0])}`)) as T;
      case "SaveNote":
        await this.request("PUT", "/api/v1/notes", {
          originalTitle: args[0],
          title: args[1],
          body: args[2],
        });
        return undefined as T;
      case "DeleteNote":
        await this.request("DELETE", `/api/v1/notes/${encodeURIComponent(args[0])}`);
        return undefined as T;
      case "GetWikiSections":
        return (await this.request("GET", "/api/v1/wiki")) as T;
      case "GetMapSections":
        return (await this.request("GET", "/api/v1/maps")) as T;
      case "OpenURL":
        this.openURL(args[0]);
        return undefined as T;
      case "OpenWikiSlug":
        this.openURL(`http://eternal-city.wikidot.com/${encodeURIComponent(args[0])}`);
        return undefined as T;
      case "CalcRankBonus":
        return (await this.request("POST", "/api/v1/calc/rank-bonus", {
          mode: args[0], basics: args[1], subskill: args[2],
        })) as T;
      case "CalcTrainCost":
        return (await this.request("POST", "/api/v1/calc/train-cost", {
          current: args[0], desired: args[1], slot: args[2], difficulty: args[3],
          selfTrained: args[4], selfTaught: args[5], healing: args[6],
        })) as T;
      case "CheckForUpdate":
        // Startup update checks are intentionally owned by the native shell;
        // do not repeat them once per connected browser.
        return fallback;
      default:
        if (settingsOperations[method]) {
          await this.updateSetting(settingsOperations[method], settingPayload(method, args));
          return undefined as T;
        }
        throw new Error(`No web transport operation for ${method}`);
    }
  }

  subscribe(handlers: TransportHandlers): () => void {
    this.handlers.add(handlers);
    return () => this.handlers.delete(handlers);
  }

  async start(): Promise<void> {
    this.started = true;
    this.socketReady = false;
    this.emitSystem({ type: "transport", transportState: "connecting" });
    this.openSocket();
  }

  async webLogin(password: string): Promise<void> {
    await this.request("POST", "/api/v1/auth/login", { password }, false);
    this.started = false;
    this.authExpired = false;
    this.reconnectAttempt = 0;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
    this.broadcastSessionChange("login");
  }

  async webLogout(): Promise<void> {
    try {
      await this.request("POST", "/api/v1/auth/logout", {});
    } catch (error) {
      // Local sign-out must remain available during a network failure. The
      // opaque HttpOnly cookie cannot be cleared client-side, but it expires
      // with the in-memory server process and a later login replaces it.
      console.warn("Praetor logout request did not complete:", error);
    } finally {
      this.broadcastSessionChange("logout");
      this.expireAuthentication("signed out");
    }
  }

  async quit(): Promise<void> {
    await this.webLogout();
  }

  showLocalNotification(title: string, message: string): void {
    if ("Notification" in window && Notification.permission === "granted") {
      new Notification(title, { body: message });
    }
  }

  async requestNotificationPermission(): Promise<NotificationPermission | "unsupported"> {
    if (!("Notification" in window) || !window.isSecureContext) return "unsupported";
    return Notification.requestPermission();
  }

  private installBootstrap(init: WebBootstrap) {
    if (init.protocol !== 1) {
      throw new Error(`Unsupported Praetor web protocol ${init.protocol}`);
    }
    if (
      typeof init.csrf !== "string" ||
      init.csrf === "" ||
      typeof init.serverId !== "string" ||
      init.serverId === "" ||
      typeof init.configRevision !== "number" ||
      !Number.isSafeInteger(init.configRevision) ||
      init.configRevision < 0
    ) {
      throw new Error("Invalid Praetor web bootstrap");
    }
    this.csrf = init.csrf;
    this.revision = init.configRevision;
    this.serverId = init.serverId;
    this.authExpired = false;
  }

  private openSocket() {
    if (!this.started || this.socket || !this.csrf) return;
    const scheme = location.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(
      `${scheme}//${location.host}/api/v1/events?sequence_ranges=1`,
    );
    const epoch = ++this.streamEpoch;
    this.socket = socket;
    socket.onopen = () => {
      this.reconnectAttempt = 0;
    };
    socket.onmessage = (event) => this.receiveSocketData(socket, epoch, event.data);
    socket.onclose = (event) => this.handleSocketClose(
      socket,
      epoch,
      event.code,
      event.reason,
    );
    socket.onerror = () => socket.close();
  }

  private receiveSocketData(socket: WebSocket, epoch: number, data: unknown) {
    const started = monotonicNow();
    const encodedBytes = typeof data === "string"
      ? UTF8_ENCODER.encode(data).byteLength
      : 0;
    let message: WebEnvelope;
    try {
      if (typeof data !== "string") {
        throw new Error("Praetor event message was not UTF-8 JSON text");
      }
      message = JSON.parse(data) as WebEnvelope;
    } catch (error) {
      const duration = monotonicNow() - started;
      this.recordParse(encodedBytes, duration);
      this.failEventSocket(
        socket,
        epoch,
        "json_parse_failure",
        error instanceof Error ? error.message : String(error),
        4002,
        "invalid event json",
      );
      return;
    }
    this.recordParse(encodedBytes, monotonicNow() - started);
    this.enqueueInbound({
      socket,
      epoch,
      envelope: message,
      encodedBytes,
      eventCount: envelopeEventCount(message),
    });
  }

  private handleSocketClose(
    socket: WebSocket,
    epoch: number,
    code: number,
    reason: string,
  ) {
    if (epoch !== this.streamEpoch) return;
    if (this.socket === socket) this.socket = null;
    this.removeQueuedEpoch(epoch);
    this.socketReady = false;
    if (!this.started) return;

    let category: WebEventStreamFailureCategory;
    if (this.localClose?.epoch === epoch) {
      category = this.localClose.category;
      this.localClose = null;
    } else if (code === 1013 && reason === "resync required") {
      category = "server_resynchronization";
      this.recordStreamFailure(category, "Server requested snapshot resynchronization");
    } else {
      category = "network_close";
      this.recordStreamFailure(category, "Praetor browser event connection closed");
    }
    this.streamDiagnostics.lastClose = {
      code,
      reason: this.safeDiagnosticMessage(reason || "no close reason"),
      category,
    };
    console.warn("Praetor event stream closed", {
      category,
      code,
      reason: this.streamDiagnostics.lastClose.reason,
    });
    this.streamDiagnostics.reconnects++;
    this.emitSystem({ type: "transport", transportState: "reconnecting" });
    this.scheduleReconnect();
  }

  private recordParse(bytes: number, duration: number) {
    this.streamDiagnostics.parsedMessages++;
    this.streamDiagnostics.parsedBytes += bytes;
    this.streamDiagnostics.maxParseMilliseconds = Math.max(
      this.streamDiagnostics.maxParseMilliseconds,
      duration,
    );
  }

  private safeDiagnosticMessage(message: string): string {
    return message.replace(/[\r\n\0]+/g, " ").slice(0, 240);
  }

  private recordStreamFailure(
    category: WebEventStreamFailureCategory,
    message: string,
  ) {
    const safeMessage = this.safeDiagnosticMessage(message);
    this.streamDiagnostics.failures[category]++;
    this.streamDiagnostics.lastFailure = { category, message: safeMessage };
    console.error("Praetor event stream failure", {
      category,
      message: safeMessage,
    });
  }

  private failEventSocket(
    socket: WebSocket,
    epoch: number,
    category: WebEventStreamFailureCategory,
    message: string,
    closeCode: number,
    closeReason: string,
  ) {
    if (epoch !== this.streamEpoch || this.localClose?.epoch === epoch) return;
    this.localClose = { epoch, category };
    this.recordStreamFailure(category, message);
    this.removeQueuedEpoch(epoch);
    socket.close(closeCode, closeReason);
  }

  private enqueueInbound(item: QueuedWebEnvelope) {
    if (item.epoch !== this.streamEpoch || this.socket !== item.socket) return;
    const boundedLiveWork = item.envelope.type !== "snapshot";
    if (boundedLiveWork && (
      this.inboundQueuedEvents + item.eventCount > WEB_INBOUND_MAX_EVENTS ||
      this.inboundQueuedBytes + item.encodedBytes > WEB_INBOUND_MAX_BYTES
    )) {
      this.failEventSocket(
        item.socket,
        item.epoch,
        "client_backlog_limit",
        "Browser event application backlog exceeded its hard bound",
        4004,
        "client backlog limit",
      );
      return;
    }
    this.inboundQueue.push(item);
    // A reconnect snapshot is the recovery mechanism for a server-side hard
    // eviction and can legitimately contain the configured retained
    // scrollback. It is still serialized and UI-chunked, but it must not consume
    // the live-work budget or the first live envelope arriving behind it could
    // force an endless reconnect loop. Only post-snapshot live work is bounded.
    if (boundedLiveWork) {
      this.inboundQueuedEvents += item.eventCount;
      this.inboundQueuedBytes += item.encodedBytes;
    }
    void this.drainInboundQueue();
  }

  private clearInboundQueue() {
    this.inboundQueue = [];
    this.inboundQueuedEvents = 0;
    this.inboundQueuedBytes = 0;
  }

  private removeQueuedEpoch(epoch: number) {
    if (this.inboundQueue.length === 0) return;
    this.inboundQueue = this.inboundQueue.filter((item) => {
      if (item.epoch !== epoch) return true;
      if (item.envelope.type !== "snapshot") {
        this.inboundQueuedEvents -= item.eventCount;
        this.inboundQueuedBytes -= item.encodedBytes;
      }
      return false;
    });
    this.inboundQueuedEvents = Math.max(0, this.inboundQueuedEvents);
    this.inboundQueuedBytes = Math.max(0, this.inboundQueuedBytes);
  }

  private releaseInbound(item: QueuedWebEnvelope) {
    if (item.envelope.type === "snapshot") return;
    this.inboundQueuedEvents = Math.max(
      0,
      this.inboundQueuedEvents - item.eventCount,
    );
    this.inboundQueuedBytes = Math.max(
      0,
      this.inboundQueuedBytes - item.encodedBytes,
    );
  }

  private async drainInboundQueue() {
    if (this.inboundProcessing) return;
    this.inboundProcessing = true;
    try {
      while (this.inboundQueue.length > 0) {
        const item = this.inboundQueue.shift()!;
        let stop = false;
        try {
          if (item.epoch !== this.streamEpoch || this.socket !== item.socket) continue;
          await this.handleEnvelope(item.envelope, item.epoch);
        } catch (error) {
          if (error instanceof StaleWebEventStreamError) continue;
          let category: WebEventStreamFailureCategory = "protocol_validation_failure";
          let closeCode = 4002;
          let closeReason = "invalid event envelope";
          if (error instanceof WebSequenceGapError) {
            category = "sequence_gap";
            closeReason = "event sequence gap";
          } else if (error instanceof WebEventHandlerError) {
            category = "event_handler_failure";
            closeCode = 4003;
            closeReason = "event handler failure";
          }
          this.failEventSocket(
            item.socket,
            item.epoch,
            category,
            error instanceof Error ? error.message : String(error),
            closeCode,
            closeReason,
          );
          stop = true;
        } finally {
          this.releaseInbound(item);
        }
        if (stop) return;
      }
    } finally {
      this.inboundProcessing = false;
      // An onmessage callback can append after the loop's final length check but
      // before the flag is lowered. Re-check once so that work cannot strand.
      if (this.inboundQueue.length > 0) void this.drainInboundQueue();
    }
  }

  private ensureCurrentEpoch(epoch?: number) {
    if (epoch !== undefined && epoch !== this.streamEpoch) {
      throw new StaleWebEventStreamError();
    }
  }

  private validateProtocol(message: WebEnvelope) {
    if (!message || typeof message !== "object") {
      throw new WebEnvelopeValidationError("Praetor event envelope is not an object");
    }
    if (message.protocol !== 1) {
      throw new WebEnvelopeValidationError(`Unsupported protocol ${message.protocol}`);
    }
    if (![
      "snapshot",
      "events",
      "config",
      "modes",
      "accounts",
      "operation",
      "commandHistory",
    ].includes(message.type)) {
      throw new WebEnvelopeValidationError(`Unsupported envelope type ${String(message.type)}`);
    }
    if (typeof message.serverId !== "string" || message.serverId === "") {
      throw new WebEnvelopeValidationError("Praetor event envelope has no server identity");
    }
    if (message.events !== undefined && !Array.isArray(message.events)) {
      throw new WebEnvelopeValidationError("Praetor event payload is not an array");
    }
  }

  private sequenceRange(message: WebEnvelope): { from: number; to: number } {
    const legacy = message.sequence;
    const from = message.fromSequence ?? legacy;
    const to = message.toSequence ?? legacy;
    if (
      !Number.isSafeInteger(from) ||
      !Number.isSafeInteger(to) ||
      (from ?? 0) <= 0 ||
      (to ?? 0) < (from ?? 0)
    ) {
      throw new WebEnvelopeValidationError("Praetor event sequence range is invalid");
    }
    if (legacy !== undefined && legacy !== to) {
      throw new WebEnvelopeValidationError("Praetor event sequence does not match range end");
    }
    if (message.type !== "events" && from !== to) {
      throw new WebEnvelopeValidationError("Authoritative state envelope cannot span a sequence range");
    }
    if (message.serverId !== this.serverId) {
      throw new WebSequenceGapError("Praetor server identity changed; resynchronizing");
    }
    if (from !== this.sequence + 1) {
      throw new WebSequenceGapError(
        `Praetor event sequence gap after ${this.sequence}; received ${from}-${to}`,
      );
    }
    return { from: from!, to: to! };
  }

  private invokeEventHandler(callback: () => void) {
    try {
      callback();
    } catch (error) {
      throw new WebEventHandlerError(error);
    }
  }

  private async yieldMainThread(epoch?: number) {
    const started = monotonicNow();
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    const duration = monotonicNow() - started;
    this.streamDiagnostics.mainThreadYields++;
    this.streamDiagnostics.maxYieldMilliseconds = Math.max(
      this.streamDiagnostics.maxYieldMilliseconds,
      duration,
    );
    this.ensureCurrentEpoch(epoch);
  }

  private scheduleRenderMeasurement(appliedAt: number, epoch?: number) {
    // Handler return measures state mutation, not the Svelte flush or output
    // layout that follows it. Observe those asynchronously so diagnostics cover
    // the complete browser path without adding a frame delay to delivery.
    void tick().then(() => {
      if (epoch !== undefined && epoch !== this.streamEpoch) return;
      const reconciledAt = monotonicNow();
      this.streamDiagnostics.reconciliationSamples++;
      this.streamDiagnostics.maxReconciliationMilliseconds = Math.max(
        this.streamDiagnostics.maxReconciliationMilliseconds,
        reconciledAt - appliedAt,
      );
      if (
        typeof requestAnimationFrame !== "function" ||
        typeof document === "undefined"
      ) return;
      requestAnimationFrame(() => {
        if (epoch !== undefined && epoch !== this.streamEpoch) return;
        const content = document.querySelector<HTMLElement>(".output .content");
        if (!content) return;
        // Reading scrollHeight makes any pending output-pane layout part of the
        // observed latency; no text or DOM content enters the diagnostics.
        void content.scrollHeight;
        this.streamDiagnostics.outputPaneRenderSamples++;
        this.streamDiagnostics.maxOutputPaneRenderMilliseconds = Math.max(
          this.streamDiagnostics.maxOutputPaneRenderMilliseconds,
          monotonicNow() - appliedAt,
        );
      });
    });
  }

  private async applyEventChunks(
    events: WireEvent[],
    snapshot: boolean,
    epoch?: number,
  ) {
    if (events.length === 0) {
      this.invokeEventHandler(() => {
        for (const handler of [...this.handlers]) {
          if (snapshot) handler.snapshot?.([]);
          else handler.events([]);
        }
      });
      return;
    }
    for (let offset = 0; offset < events.length; offset += WEB_EVENT_APPLY_CHUNK_SIZE) {
      this.ensureCurrentEpoch(epoch);
      const chunk = events.slice(offset, offset + WEB_EVENT_APPLY_CHUNK_SIZE);
      this.invokeEventHandler(() => {
        for (const handler of [...this.handlers]) {
          if (snapshot && offset === 0) {
            handler.snapshot?.(chunk);
          } else if (!snapshot || handler.snapshot) {
            handler.events(chunk);
          }
        }
      });
      this.scheduleRenderMeasurement(monotonicNow(), epoch);
      if (offset + chunk.length < events.length) {
        await this.yieldMainThread(epoch);
      }
    }
  }

  private async handleEnvelope(message: WebEnvelope, epoch?: number) {
    const started = monotonicNow();
    this.ensureCurrentEpoch(epoch);
    this.validateProtocol(message);
    if (message.type === "snapshot") {
      const snapshotSequence = message.sequence ?? 0;
      if (!Number.isSafeInteger(snapshotSequence) || snapshotSequence < 0) {
        throw new WebEnvelopeValidationError("Praetor snapshot sequence is invalid");
      }
      if (message.config) {
        this.revision = message.revision ?? this.revision;
        this.invokeEventHandler(() => this.emitSystem({
          type: "config",
          config: message.config,
          revision: this.revision,
        }));
      }
      if (message.modeNames) {
        this.invokeEventHandler(() => this.emitSystem({
          type: "modes",
          modeNames: message.modeNames,
        }));
      }
      if (message.accounts || message.credentialStore) {
        this.invokeEventHandler(() => this.emitSystem({
          type: "accounts",
          accounts: message.accounts ?? [],
          credentialStore: message.credentialStore,
        }));
      }
      await this.applyEventChunks(message.events ?? [], true, epoch);
      if (message.commandHistory) {
        this.invokeEventHandler(() => this.emitSystem({
          type: "command-history",
          commandHistory: message.commandHistory,
        }));
      }
      this.ensureCurrentEpoch(epoch);
      this.serverId = message.serverId;
      this.sequence = snapshotSequence;
      this.socketReady = true;
      this.invokeEventHandler(() => this.emitSystem({
        type: "transport",
        transportState: "connected",
      }));
      this.streamDiagnostics.appliedEnvelopes++;
      this.streamDiagnostics.appliedEvents += message.events?.length ?? 0;
      this.streamDiagnostics.maxApplicationMilliseconds = Math.max(
        this.streamDiagnostics.maxApplicationMilliseconds,
        monotonicNow() - started,
      );
      return;
    }
    const range = this.sequenceRange(message);
    if (message.type === "events") {
      await this.applyEventChunks(message.events ?? [], false, epoch);
    } else if (message.type === "config" && message.config) {
      const revision = message.revision ?? this.revision;
      // A mutation response can reach its initiating browser before an older
      // queued WebSocket broadcast. Consume the sequence but never roll the
      // browser's authoritative config revision backward.
      if (revision >= this.revision) {
        this.revision = revision;
        this.invokeEventHandler(() => this.emitSystem({
          type: "config",
          config: message.config,
          revision,
        }));
      }
    } else if (message.type === "modes") {
      this.invokeEventHandler(() => this.emitSystem({
        type: "modes",
        modeNames: message.modeNames ?? [],
        result: message.result,
      }));
    } else if (message.type === "accounts") {
      this.invokeEventHandler(() => this.emitSystem({
        type: "accounts",
        accounts: message.accounts ?? [],
        credentialStore: message.credentialStore,
      }));
    } else if (message.type === "operation") {
      this.invokeEventHandler(() => this.emitSystem({
        type: "operation",
        result: message.result,
      }));
    } else if (message.type === "commandHistory" && message.commandHistory) {
      this.invokeEventHandler(() => this.emitSystem({
        type: "command-history",
        commandHistory: message.commandHistory,
      }));
    }
    this.ensureCurrentEpoch(epoch);
    this.sequence = range.to;
    this.streamDiagnostics.appliedEnvelopes++;
    this.streamDiagnostics.appliedEvents += message.events?.length ?? 0;
    this.streamDiagnostics.maxApplicationMilliseconds = Math.max(
      this.streamDiagnostics.maxApplicationMilliseconds,
      monotonicNow() - started,
    );
  }

  private emitSystem(update: SystemUpdate) {
    for (const handler of this.handlers) handler.system?.(update);
  }

  private scheduleReconnect() {
    if (this.reconnectTimer) return;
    const base = Math.min(30000, 500 * 2 ** this.reconnectAttempt++);
    const delay = base + Math.floor(Math.random() * Math.max(100, base / 4));
    this.reconnectTimer = setTimeout(async () => {
      this.reconnectTimer = null;
      try {
        const init = await this.request<WebBootstrap>("GET", "/api/v1/bootstrap");
        this.installBootstrap(init);
        this.openSocket();
      } catch (error) {
        if (error instanceof WebAuthRequiredError) {
          this.expireAuthentication("authentication expired");
        } else if (this.started) {
          this.scheduleReconnect();
        }
      }
    }, delay);
  }

  private async request<T = unknown>(
    method: string,
    url: string,
    body?: unknown,
    authenticated = true,
  ): Promise<T> {
    const response = await this.fetchResponse(method, url, body, authenticated);
    if (response.status === 204) return undefined as T;
    return (await response.json()) as T;
  }

  private async fetchResponse(
    method: string,
    url: string,
    body?: unknown,
    authenticated = true,
    allowCSRFRecovery = true,
    bypassSocketGate = false,
  ): Promise<Response> {
    if (
      authenticated &&
      method !== "GET" &&
      method !== "HEAD" &&
      url !== "/api/v1/auth/logout" &&
      this.started &&
      !this.socketReady &&
      !bypassSocketGate
    ) {
      throw new Error("Praetor is reconnecting; wait for current state before making changes.");
    }
    const headers: Record<string, string> = { Accept: "application/json" };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const mutating = method !== "GET" && method !== "HEAD";
    const sentCSRF = authenticated && mutating ? this.csrf : "";
    if (sentCSRF) headers["X-Praetor-CSRF"] = sentCSRF;
    const response = await fetch(url, {
      method,
      headers,
      credentials: "same-origin",
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (response.status === 401 && authenticated) {
      this.expireAuthentication("authentication expired");
      throw new WebAuthRequiredError();
    }
    if (!response.ok) {
      const error = await responseError(response);
      if (
        authenticated &&
        mutating &&
        allowCSRFRecovery &&
        error.status === 403 &&
        error.code === "csrf_rejected"
      ) {
        let serverChanged = false;
        // Another concurrent request may already have installed the new token
        // by the time this rejection arrives. In that case retry directly
        // instead of performing a redundant bootstrap.
        if (!sentCSRF || sentCSRF === this.csrf) {
          ({ serverChanged } = await this.refreshBootstrap());
        }
        if (serverChanged) {
          this.restartSocket("server restarted");
          throw new Error(
            "Praetor restarted and is resynchronizing. Review current state, then try again.",
          );
        }
        // A same-profile login signal may be replacing the event socket at
        // this point. The one replay is still safe: the rejected request never
        // reached its handler, the server process is unchanged, and the fresh
        // bootstrap authenticated the cookie now used by fetch.
        return this.fetchResponse(
          method,
          url,
          body,
          authenticated,
          false,
          true,
        );
      }
      throw error;
    }
    return response;
  }

  private async refreshBootstrap(): Promise<BootstrapRefresh> {
    if (this.csrfRefresh) return this.csrfRefresh;
    this.csrfRefresh = (async () => {
      const previousServerID = this.serverId;
      const response = await this.fetchResponse(
        "GET",
        "/api/v1/bootstrap",
        undefined,
        true,
        false,
      );
      let init: WebBootstrap;
      try {
        init = (await response.json()) as WebBootstrap;
        this.installBootstrap(init);
      } catch {
        this.expireAuthentication("invalid authentication bootstrap");
        throw new WebAuthRequiredError();
      }
      return {
        init,
        serverChanged:
          previousServerID !== "" && previousServerID !== init.serverId,
      };
    })();
    try {
      return await this.csrfRefresh;
    } finally {
      this.csrfRefresh = null;
    }
  }

  private async handleSessionSignal(signal: SessionSignal) {
    if (signal.action === "logout") {
      this.expireAuthentication("signed out in another tab");
      return;
    }
    try {
      const { serverChanged } = await this.refreshBootstrap();
      if (this.started) {
        this.restartSocket(
          serverChanged ? "server session changed" : "browser session changed",
        );
      } else {
        this.emitSystem({ type: "auth-restored" });
      }
    } catch (error) {
      if (!(error instanceof WebAuthRequiredError)) {
        console.warn("Praetor session refresh did not complete:", error);
      }
    }
  }

  private restartSocket(reason: string) {
    const socket = this.socket;
    this.socket = null;
    this.socketReady = false;
    // Retire the outgoing socket's epoch so its queued envelopes and late
    // close event cannot touch the replacement stream.
    this.streamEpoch++;
    this.clearInboundQueue();
    this.localClose = null;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
    socket?.close(1000, reason);
    if (this.started) {
      this.emitSystem({ type: "transport", transportState: "reconnecting" });
      this.openSocket();
    }
  }

  private expireAuthentication(reason: string) {
    this.started = false;
    this.streamEpoch++;
    this.clearInboundQueue();
    this.localClose = null;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
    this.socket?.close(1000, reason);
    this.socket = null;
    this.csrf = "";
    this.serverId = "";
    this.sequence = 0;
    this.socketReady = false;
    this.csrfRefresh = null;
    if (!this.authExpired) {
      this.authExpired = true;
      this.emitSystem({ type: "auth-expired" });
    }
  }

  private broadcastSessionChange(action: SessionSignal["action"]) {
    try {
      this.sessionChannel?.postMessage({
        type: "praetor-session-changed",
        action,
        source: this.sessionSource,
        generation: randomSessionMarker(),
      } satisfies SessionSignal);
    } catch {
      // Other tabs still recover reactively if a channel closes unexpectedly.
    }
  }

  private async updateSetting(operation: string, value: unknown) {
    const response = await this.request<ConfigMutationResponse>(
      "PUT",
      `/api/v1/settings/${operation}`,
      { expectedRevision: this.revision, value },
    );
    this.acceptConfigMutation(response);
  }

  private acceptConfigMutation(response: ConfigMutationResponse) {
    this.revision = response.revision;
    this.emitSystem({ type: "config", config: response.config, revision: response.revision });
  }

  private async downloadPersistent(keys: string[]): Promise<string> {
    const response = await this.fetchResponse(
      "POST",
      "/api/v1/persistent/export",
      { keys },
    );
    const blob = await response.blob();
    const disposition = response.headers.get("Content-Disposition") ?? "";
    const filename = disposition.match(/filename="?([^";]+)"?/)?.[1] ?? "persistent.json";
    const objectURL = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = objectURL;
    link.download = filename;
    link.click();
    setTimeout(() => URL.revokeObjectURL(objectURL), 0);
    return filename;
  }

  private openURL(url: string) {
    const parsed = new URL(url, window.location.href);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") throw new Error("Unsupported URL scheme");
    window.open(parsed.toString(), "_blank", "noopener,noreferrer");
  }

  private copyFallback(value: string) {
    const field = document.createElement("textarea");
    field.value = value;
    field.setAttribute("readonly", "");
    field.style.position = "fixed";
    field.style.opacity = "0";
    document.body.appendChild(field);
    field.select();
    const copied = document.execCommand("copy");
    field.remove();
    if (!copied) throw new Error("Browser clipboard write is unavailable; copy the selected text manually.");
  }
}

async function responseError(response: Response): Promise<WebAPIError> {
  let detail: ErrorResponse = {};
  try {
    detail = await response.json();
  } catch {
    // Use the status fallback when the response is not a typed API error.
  }
  return new WebAPIError(
    response.status,
    detail.error?.code ?? "request_failed",
    detail.error?.message ?? `Request failed (${response.status})`,
  );
}

function parseSessionSignal(value: unknown): SessionSignal | null {
  if (!value || typeof value !== "object") return null;
  const signal = value as Partial<SessionSignal>;
  if (
    signal.type !== "praetor-session-changed" ||
    (signal.action !== "login" && signal.action !== "logout") ||
    typeof signal.source !== "string" ||
    signal.source === "" ||
    typeof signal.generation !== "string" ||
    signal.generation === ""
  ) {
    return null;
  }
  return signal as SessionSignal;
}

function randomSessionMarker(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  const values = new Uint32Array(4);
  if (typeof crypto !== "undefined" && typeof crypto.getRandomValues === "function") {
    crypto.getRandomValues(values);
    return [...values].map((value) => value.toString(16).padStart(8, "0")).join("");
  }
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
}

interface ConfigMutationResponse {
  config: AppConfig;
  revision: number;
}

export const settingsOperations: Record<string, string> = {
  SetEchoTyped: "echo-typed",
  SetEchoScript: "echo-script",
  SetColorWords: "color-words",
  SetHideIPs: "hide-ips",
  SetInputSpellcheck: "input-spellcheck",
  SetUpdateCheck: "update-check",
  SetMobileShowToolbar: "mobile-show-toolbar",
  SetMobileShowTabBar: "mobile-show-tab-bar",
  SetMobileHideNavigationOnInput: "mobile-hide-navigation-on-input",
  SetMobileLowercaseFirstLetter: "mobile-lowercase-first-letter",
  SetMobileOutputFontSize: "mobile-output-font-size",
  SetRetainAppLogs: "retain-app-logs",
  SetSessionLogging: "session-logging",
  SetLogPath: "log-path",
  SetDisplayMode: "display-mode",
  SetNumpadNavigation: "numpad-navigation",
  SetMinimapScale: "minimap-scale",
  SetCompassScale: "compass-scale",
  SetOutputFontSize: "output-font-size",
  SetCRTEffects: "crt-effects",
  SetHighlights: "highlights",
  SetCustomTabs: "custom-tabs",
  SetActionSets: "action-sets",
  SetQuickCycleModes: "quick-cycle-modes",
  SetHighPriority: "high-priority",
  SetIgnoreOOC: "ignore-ooc",
  SetIgnoreThink: "ignore-think",
  SetNotifications: "notifications",
  SetScriptDirs: "script-directories",
};

export const WEB_SUPPORTED_METHODS = new Set([
  "GetInitState",
  "GetConfig",
  "ListAccounts",
  "ConnectNew",
  "ConnectStored",
  "SaveAccount",
  "RemoveAccount",
  "Disconnect",
  "Send",
  "SubmitTypedCommand",
  "ModeNames",
  "CurrentMode",
  "SetMode",
  "ReloadScripts",
  "PickScriptDir",
  "PickSendFile",
  "StartFileSend",
  "AbortSend",
  "PickPlayFile",
  "StartPlay",
  "PausePlay",
  "ResumePlay",
  "StopPlay",
  "NextPlayStep",
  "PlayActive",
  "PlayStatus",
  "RefreshGraphics",
  "ClipboardGet",
  "ClipboardSet",
  "GetKudos",
  "SetKudos",
  "AddKudosFavorite",
  "AddKudosQueue",
  "GetPersistentData",
  "ExportPersistentData",
  "ClearPersistentData",
  "ListNotes",
  "GetNote",
  "SaveNote",
  "DeleteNote",
  "GetWikiSections",
  "GetMapSections",
  "OpenURL",
  "OpenWikiSlug",
  "CalcRankBonus",
  "CalcTrainCost",
  "CheckForUpdate",
  ...Object.keys(settingsOperations),
]);

function settingPayload(method: string, args: any[]): unknown {
  if (method === "SetCRTEffects") {
    return { scanlines: args[0], roll: args[1], bloom: args[2] };
  }
  return args[0];
}
