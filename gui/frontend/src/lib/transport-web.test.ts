import { afterEach, describe, expect, it, vi } from "vitest";
// The production tsconfig intentionally omits Node types; Vitest still runs in
// Node and provides this built-in for the source-level parity contract.
import { readFileSync } from "node:fs";
import { settingsOperations, WEB_SUPPORTED_METHODS, WebTransport } from "./transport-web";
import { WebAuthRequiredError } from "./transport";
import { Kind } from "./types";

describe("web transport operation parity", () => {
  afterEach(() => {
    FakeBroadcastChannel.reset();
    vi.unstubAllGlobals();
  });

  it("has an explicit web decision for every transport-neutral bridge call", () => {
    const source = readFileSync(new URL("./bridge.ts", import.meta.url), "utf8");
    const methods = new Set<string>();
    for (const match of source.matchAll(/call(?:<[^>]+>)?\(\s*"([^"]+)"/g)) {
      methods.add(match[1]);
    }

    expect(methods.size).toBeGreaterThan(20);
    expect([...methods].filter((method) => !WEB_SUPPORTED_METHODS.has(method))).toEqual([]);
  });

  it("fails closed for an unknown operation", async () => {
    const transport = new WebTransport();
    await expect(transport.invoke("FutureWailsOnlyMethod", undefined)).rejects.toThrow(
      "No web transport operation",
    );
  });

  it("maps shared and mobile preferences to revisioned setting operations", () => {
    expect(settingsOperations).toMatchObject({
      SetInputSpellcheck: "input-spellcheck",
      SetUpdateCheck: "update-check",
      SetMobileShowToolbar: "mobile-show-toolbar",
      SetMobileShowTabBar: "mobile-show-tab-bar",
      SetMobileHideNavigationOnInput: "mobile-hide-navigation-on-input",
      SetMobileLowercaseFirstLetter: "mobile-lowercase-first-letter",
      SetMobileOutputFontSize: "mobile-output-font-size",
      SetRetainAppLogs: "retain-app-logs",
    });
  });

  it("installs a snapshot before ordered live events and rejects a gap", async () => {
    const transport = new WebTransport();
    const received: string[] = [];
    transport.subscribe({
      snapshot: (events) => received.push(`snapshot:${events[0]?.text?.text}`),
      events: (events) => received.push(`events:${events[0]?.text?.text}`),
      system: (update) => {
        if (update.type === "transport") received.push(`transport:${update.transportState}`);
      },
    });
    const envelope = (sequence: number, text: string) => ({
      type: "events",
      protocol: 1,
      serverId: "server-a",
      sequence,
      events: [{ kind: Kind.Text, text: { text, segments: [{ text }] } }],
    });

    await (transport as any).handleEnvelope({
      type: "snapshot",
      protocol: 1,
      serverId: "server-a",
      sequence: 4,
      events: [{ kind: Kind.Text, text: { text: "before", segments: [{ text: "before" }] } }],
    });
    await (transport as any).handleEnvelope(envelope(5, "after"));

    expect(received).toEqual(["snapshot:before", "transport:connected", "events:after"]);
    await expect((transport as any).handleEnvelope(envelope(7, "gap"))).rejects.toThrow(
      "sequence gap",
    );
  });

  it("applies a contiguous coalesced range in bounded ordered UI chunks", async () => {
    const transport = new WebTransport();
    const received: number[] = [];
    const chunkSizes: number[] = [];
    let sequenceDuringApplication = -1;
    transport.subscribe({
      snapshot: () => {},
      events: (events) => {
        sequenceDuringApplication = (transport as any).sequence;
        chunkSizes.push(events.length);
        received.push(...events.map((event) => Number(event.text?.text)));
      },
    });
    await (transport as any).handleEnvelope({
      type: "snapshot",
      protocol: 1,
      serverId: "server-a",
      sequence: 10,
      events: [],
    });
    const events = Array.from({ length: 250 }, (_, index) => ({
      kind: Kind.Text,
      text: { text: String(index), segments: [{ text: String(index) }] },
    }));

    await (transport as any).handleEnvelope({
      type: "events",
      protocol: 1,
      serverId: "server-a",
      sequence: 13,
      fromSequence: 11,
      toSequence: 13,
      events,
    });

    expect(chunkSizes).toEqual([100, 100, 50]);
    expect(received).toEqual(Array.from({ length: 250 }, (_, index) => index));
    expect(sequenceDuringApplication).toBe(10);
    expect((transport as any).sequence).toBe(13);
    expect(transport.eventStreamDiagnostics()).toMatchObject({
      appliedEnvelopes: 2,
      appliedEvents: 250,
      mainThreadYields: 2,
    });
  });

  it("batches hundreds of adjacent one-envelope lines before UI reconciliation", async () => {
    const transport = new WebTransport();
    const chunkSizes: number[] = [];
    const received: number[] = [];
    const closes: Array<[number, string]> = [];
    const socket = {
      close: (code: number, reason: string) => closes.push([code, reason]),
    } as unknown as WebSocket;
    transport.subscribe({
      snapshot: () => {},
      events: (events) => {
        chunkSizes.push(events.length);
        received.push(...events.map((event) => Number(event.text?.text)));
      },
    });
    await (transport as any).handleEnvelope({
      type: "snapshot",
      protocol: 1,
      serverId: "server-a",
      sequence: 0,
      events: [],
    });
    (transport as any).streamEpoch = 1;
    (transport as any).socket = socket;

    for (let sequence = 1; sequence <= 217; sequence++) {
      (transport as any).receiveSocketData(socket, 1, JSON.stringify({
        type: "events",
        protocol: 1,
        serverId: "server-a",
        sequence,
        fromSequence: sequence,
        toSequence: sequence,
        events: [{
          kind: Kind.Text,
          text: { text: String(sequence), segments: [{ text: String(sequence) }] },
        }],
      }));
    }

    await vi.waitFor(() => expect((transport as any).sequence).toBe(217));
    expect(closes).toEqual([]);
    expect(chunkSizes).toEqual([100, 100, 17]);
    expect(received).toEqual(Array.from({ length: 217 }, (_, index) => index + 1));
    expect(transport.eventStreamDiagnostics()).toMatchObject({
      appliedEnvelopes: 218,
      appliedEvents: 217,
      mainThreadYields: 2,
    });
  });

  it("resumes retained browser state without reinstalling scrollback", async () => {
    const transport = new WebTransport();
    const snapshots: string[][] = [];
    const live: string[] = [];
    const states: string[] = [];
    transport.subscribe({
      snapshot: (events) => snapshots.push(
        events.map((event) => event.text?.text ?? ""),
      ),
      events: (events) => live.push(
        ...events.map((event) => event.text?.text ?? ""),
      ),
      system: (update) => {
        if (update.type === "transport") states.push(update.transportState ?? "");
      },
    });
    await (transport as any).handleEnvelope({
      type: "snapshot",
      protocol: 1,
      serverId: "server-a",
      sequence: 4,
      events: [{
        kind: Kind.Text,
        text: { text: "retained", segments: [{ text: "retained" }] },
      }],
    });
    await (transport as any).handleEnvelope({
      type: "events",
      protocol: 1,
      serverId: "server-a",
      sequence: 5,
      fromSequence: 5,
      toSequence: 5,
      events: [{
        kind: Kind.Text,
        text: { text: "before-close", segments: [{ text: "before-close" }] },
      }],
    });
    await (transport as any).handleEnvelope({
      type: "resume",
      protocol: 1,
      serverId: "server-a",
      sequence: 5,
    });
    await (transport as any).handleEnvelope({
      type: "events",
      protocol: 1,
      serverId: "server-a",
      sequence: 6,
      fromSequence: 6,
      toSequence: 6,
      events: [{
        kind: Kind.Text,
        text: { text: "missed-while-closed", segments: [{ text: "missed-while-closed" }] },
      }],
    });

    expect(snapshots).toEqual([["retained"]]);
    expect(live).toEqual(["before-close", "missed-while-closed"]);
    expect(states).toEqual(["connected", "connected"]);
    expect((transport as any).sequence).toBe(6);
  });

  it("requests sequence resumption only after an authoritative snapshot", () => {
    const urls: string[] = [];
    class FakeWebSocket {
      onopen: (() => void) | null = null;
      onmessage: ((event: MessageEvent) => void) | null = null;
      onclose: ((event: CloseEvent) => void) | null = null;
      onerror: (() => void) | null = null;
      constructor(url: string) {
        urls.push(url);
      }
      close() {}
    }
    vi.stubGlobal("location", { protocol: "https:", host: "praetor.test" });
    vi.stubGlobal("WebSocket", FakeWebSocket);
    const transport = new WebTransport();
    Object.assign(transport as any, {
      started: true,
      csrf: "csrf",
      serverId: "server-a",
      sequence: 42,
      canResume: true,
    });

    (transport as any).openSocket();

    expect(urls).toHaveLength(1);
    const url = new URL(urls[0]);
    expect(url.searchParams.get("sequence_ranges")).toBe("1");
    expect(url.searchParams.get("resume_server_id")).toBe("server-a");
    expect(url.searchParams.get("after_sequence")).toBe("42");
  });

  it("measures reconciliation and output-pane layout after applied chunks", async () => {
    vi.stubGlobal("document", {
      querySelector: vi.fn(() => ({ scrollHeight: 4096 })),
    });
    vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => {
      queueMicrotask(() => callback(performance.now()));
      return 1;
    });
    const transport = new WebTransport();
    transport.subscribe({
      snapshot: () => {},
      events: () => {},
    });

    await (transport as any).handleEnvelope({
      type: "snapshot",
      protocol: 1,
      serverId: "server-a",
      sequence: 0,
      events: [],
    });
    await (transport as any).handleEnvelope({
      type: "events",
      protocol: 1,
      serverId: "server-a",
      sequence: 1,
      fromSequence: 1,
      toSequence: 1,
      events: [{
        kind: Kind.Text,
        text: { text: "inventory", segments: [{ text: "inventory" }] },
      }],
    });

    await vi.waitFor(() => {
      expect(transport.eventStreamDiagnostics()).toMatchObject({
        reconciliationSamples: 1,
        outputPaneRenderSamples: 1,
      });
    });
    const diagnostics = transport.eventStreamDiagnostics();
    expect(diagnostics.maxReconciliationMilliseconds).toBeGreaterThanOrEqual(0);
    expect(diagnostics.maxOutputPaneRenderMilliseconds).toBeGreaterThanOrEqual(0);
  });

  it("keeps sequence unchanged when event application fails", async () => {
    const transport = new WebTransport();
    transport.subscribe({
      snapshot: () => {},
      events: () => { throw new Error("renderer exploded"); },
    });
    await (transport as any).handleEnvelope({
      type: "snapshot",
      protocol: 1,
      serverId: "server-a",
      sequence: 4,
      events: [],
    });

    await expect((transport as any).handleEnvelope({
      type: "events",
      protocol: 1,
      serverId: "server-a",
      sequence: 5,
      fromSequence: 5,
      toSequence: 5,
      events: [{ kind: Kind.Text, text: { text: "line", segments: [{ text: "line" }] } }],
    })).rejects.toMatchObject({ name: "WebEventHandlerError" });
    expect((transport as any).sequence).toBe(4);
  });

  it("serializes socket messages while a large range yields to the UI", async () => {
    const transport = new WebTransport();
    const order: string[] = [];
    const closes: Array<[number, string]> = [];
    const socket = {
      close: (code: number, reason: string) => closes.push([code, reason]),
    } as unknown as WebSocket;
    (transport as any).streamEpoch = 1;
    (transport as any).socket = socket;
    transport.subscribe({
      snapshot: () => order.push("snapshot"),
      events: (events) => order.push(`events:${events[0]?.text?.text}`),
      system: (update) => {
        if (update.type === "modes") order.push("modes");
      },
    });
    const send = (envelope: object) => (transport as any).receiveSocketData(
      socket,
      1,
      JSON.stringify(envelope),
    );
    send({
      type: "snapshot",
      protocol: 1,
      serverId: "server-a",
      sequence: 0,
      events: [],
    });
    send({
      type: "events",
      protocol: 1,
      serverId: "server-a",
      sequence: 1,
      fromSequence: 1,
      toSequence: 1,
      events: Array.from({ length: 250 }, (_, index) => ({
        kind: Kind.Text,
        text: { text: String(index), segments: [{ text: String(index) }] },
      })),
    });
    send({
      type: "modes",
      protocol: 1,
      serverId: "server-a",
      sequence: 2,
      modeNames: ["idle"],
    });

    await vi.waitFor(() => expect((transport as any).sequence).toBe(2));
    expect(closes).toEqual([]);
    expect(order).toEqual([
      "snapshot",
      "events:0",
      "events:100",
      "events:200",
      "modes",
    ]);
  });

  it("reports handler failures separately from malformed JSON", async () => {
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const handlerTransport = new WebTransport();
    const handlerCloses: Array<[number, string]> = [];
    const handlerSocket = {
      close: (code: number, reason: string) => handlerCloses.push([code, reason]),
    } as unknown as WebSocket;
    (handlerTransport as any).streamEpoch = 1;
    (handlerTransport as any).socket = handlerSocket;
    handlerTransport.subscribe({
      events: () => {},
      snapshot: () => { throw new Error("output renderer failure"); },
    });
    (handlerTransport as any).receiveSocketData(
      handlerSocket,
      1,
      JSON.stringify({
        type: "snapshot",
        protocol: 1,
        serverId: "server-a",
        sequence: 0,
        events: [],
      }),
    );
    await vi.waitFor(() => expect(handlerCloses).toEqual([[4003, "event handler failure"]]));
    expect(handlerTransport.eventStreamDiagnostics().lastFailure?.category).toBe(
      "event_handler_failure",
    );
    expect(handlerTransport.eventStreamDiagnostics().failures.json_parse_failure).toBe(0);

    const parseTransport = new WebTransport();
    const parseCloses: Array<[number, string]> = [];
    const parseSocket = {
      close: (code: number, reason: string) => parseCloses.push([code, reason]),
    } as unknown as WebSocket;
    (parseTransport as any).streamEpoch = 1;
    (parseTransport as any).socket = parseSocket;
    (parseTransport as any).receiveSocketData(parseSocket, 1, "{not json");
    expect(parseCloses).toEqual([[4002, "invalid event json"]]);
    expect(parseTransport.eventStreamDiagnostics().lastFailure?.category).toBe(
      "json_parse_failure",
    );
    expect(parseTransport.eventStreamDiagnostics().failures.event_handler_failure).toBe(0);
    errorSpy.mockRestore();
  });

  it("records server resynchronization and network closes with code and reason", () => {
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
    const serverTransport = new WebTransport();
    const socket = { close: () => {} } as unknown as WebSocket;
    (serverTransport as any).started = true;
    (serverTransport as any).streamEpoch = 3;
    (serverTransport as any).socket = socket;
    (serverTransport as any).scheduleReconnect = vi.fn();
    (serverTransport as any).handleSocketClose(
      socket,
      3,
      1013,
      "resync required",
    );
    expect(serverTransport.eventStreamDiagnostics().lastClose).toEqual({
      code: 1013,
      reason: "resync required",
      category: "server_resynchronization",
    });

    const networkTransport = new WebTransport();
    (networkTransport as any).started = true;
    (networkTransport as any).streamEpoch = 7;
    (networkTransport as any).socket = socket;
    (networkTransport as any).scheduleReconnect = vi.fn();
    (networkTransport as any).handleSocketClose(socket, 7, 1006, "");
    expect(networkTransport.eventStreamDiagnostics().lastClose).toEqual({
      code: 1006,
      reason: "no close reason",
      category: "network_close",
    });
    errorSpy.mockRestore();
    warnSpy.mockRestore();
  });

  it("bounds parsed browser work while preserving snapshot resynchronization", () => {
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const transport = new WebTransport();
    const closes: Array<[number, string]> = [];
    const socket = {
      close: (code: number, reason: string) => closes.push([code, reason]),
    } as unknown as WebSocket;
    (transport as any).streamEpoch = 1;
    (transport as any).socket = socket;
    (transport as any).receiveSocketData(
      socket,
      1,
      JSON.stringify({
        type: "events",
        protocol: 1,
        serverId: "server-a",
        sequence: 1,
        fromSequence: 1,
        toSequence: 1,
        events: Array.from({ length: 8193 }, (_, index) => ({
          kind: Kind.Text,
          text: { text: String(index), segments: [{ text: String(index) }] },
        })),
      }),
    );
    expect(closes).toEqual([[4004, "client backlog limit"]]);
    expect(transport.eventStreamDiagnostics().lastFailure?.category).toBe(
      "client_backlog_limit",
    );
    expect((transport as any).inboundQueue).toEqual([]);
    errorSpy.mockRestore();
  });

  it("does not count a chunking reconnect snapshot against queued live work", async () => {
    const transport = new WebTransport();
    const closes: Array<[number, string]> = [];
    const received: string[] = [];
    const socket = {
      close: (code: number, reason: string) => closes.push([code, reason]),
    } as unknown as WebSocket;
    (transport as any).streamEpoch = 1;
    (transport as any).socket = socket;
    transport.subscribe({
      snapshot: (events) => received.push(
        ...events.map((event) => event.text?.text ?? ""),
      ),
      events: (events) => received.push(
        ...events.map((event) => event.text?.text ?? ""),
      ),
    });

    const retained = Array.from({ length: 8193 }, (_, index) => ({
      kind: Kind.Text,
      text: {
        text: `retained ${index}`,
        segments: [{ text: `retained ${index}` }],
      },
    }));
    const send = (envelope: object) => (transport as any).receiveSocketData(
      socket,
      1,
      JSON.stringify(envelope),
    );
    send({
      type: "snapshot",
      protocol: 1,
      serverId: "server-a",
      sequence: 12,
      events: retained,
    });
    // This arrives while snapshot application is yielding. It must remain in
    // source order without treating the retained snapshot as live backlog.
    send({
      type: "events",
      protocol: 1,
      serverId: "server-a",
      sequence: 13,
      fromSequence: 13,
      toSequence: 13,
      events: [{
        kind: Kind.Text,
        text: { text: "live after snapshot", segments: [{ text: "live after snapshot" }] },
      }],
    });

    await vi.waitFor(() => expect((transport as any).sequence).toBe(13), {
      timeout: 3000,
    });
    expect(closes).toEqual([]);
    expect(received).toHaveLength(retained.length + 1);
    expect(received[0]).toBe("retained 0");
    expect(received[retained.length - 1]).toBe(`retained ${retained.length - 1}`);
    expect(received[retained.length]).toBe("live after snapshot");
    expect((transport as any).inboundQueuedEvents).toBe(0);
    expect((transport as any).inboundQueuedBytes).toBe(0);
  });

  it.each([100, 250, 500, 1000])(
    "applies a %i-line production-shaped burst without loss",
    async (count) => {
      const transport = new WebTransport();
      let visible: string[] = [];
      transport.subscribe({
        snapshot: (events) => {
          visible = events.map((event) => event.text?.text ?? "");
        },
        events: (events) => {
          visible.push(...events.map((event) => event.text?.text ?? ""));
        },
      });
      await (transport as any).handleEnvelope({
        type: "snapshot",
        protocol: 1,
        serverId: "server-a",
        sequence: 0,
        events: [],
      });
      const events = Array.from({ length: count }, (_, index) => ({
        kind: Kind.Text,
        text: {
          text: `inventory ${index}`,
          segments: [{ text: `inventory ${index}` }],
        },
      }));
      await (transport as any).handleEnvelope({
        type: "events",
        protocol: 1,
        serverId: "server-a",
        sequence: 1,
        fromSequence: 1,
        toSequence: 1,
        events,
      });
      expect(visible).toHaveLength(count);
      expect(visible[0]).toBe("inventory 0");
      expect(visible[count - 1]).toBe(`inventory ${count - 1}`);

      // A reconnect snapshot replaces, rather than appends to, retained output.
      await (transport as any).handleEnvelope({
        type: "snapshot",
        protocol: 1,
        serverId: "server-b",
        sequence: 8,
        events,
      });
      expect(visible).toHaveLength(count);
      expect(new Set(visible).size).toBe(count);
    },
  );

  it("does not roll config backward when an older broadcast follows a mutation response", async () => {
    const transport = new WebTransport();
    const revisions: number[] = [];
    transport.subscribe({
      events: () => {},
      system: (update) => {
        if (update.type === "config" && update.revision !== undefined) revisions.push(update.revision);
      },
    });
    const config = {} as any;
    await (transport as any).handleEnvelope({
      type: "snapshot", protocol: 1, serverId: "server-a", sequence: 1,
      revision: 1, config,
    });
    (transport as any).acceptConfigMutation({ revision: 3, config });
    await (transport as any).handleEnvelope({
      type: "config", protocol: 1, serverId: "server-a", sequence: 2,
      revision: 2, config,
    });

    expect(revisions).toEqual([1, 3]);
  });

  it("submits typed commands and folds HTTP plus ordered socket history updates", async () => {
    const fetchMock = vi.fn(async (
      _input: RequestInfo | URL,
      _init?: RequestInit,
    ) => new Response(JSON.stringify({
      history: {
        epoch: 3,
        revision: 2,
        entry: { id: 2, text: "look" },
      },
    }), { status: 202, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    const transport = new WebTransport();
    const updates: any[] = [];
    transport.subscribe({
      events: () => {},
      system: (update) => {
        if (update.type === "command-history") updates.push(update.commandHistory);
      },
    });
    await (transport as any).handleEnvelope({
      type: "snapshot",
      protocol: 1,
      serverId: "server-a",
      sequence: 5,
      commandHistory: {
        epoch: 3,
        revision: 1,
        replace: true,
        entries: [{ id: 1, text: "skills" }],
      },
    });

    await transport.invoke(
      "SubmitTypedCommand",
      undefined,
      "look",
      "game",
      "browser-request-1",
    );
    await (transport as any).handleEnvelope({
      type: "commandHistory",
      protocol: 1,
      serverId: "server-a",
      sequence: 6,
      commandHistory: {
        epoch: 3,
        revision: 2,
        entry: { id: 2, text: "look" },
      },
    });

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/v1/typed-commands");
    const request = fetchMock.mock.calls[0]?.[1] as RequestInit;
    expect(request.method).toBe("POST");
    expect(JSON.parse(String(request.body))).toEqual({
      input: "look",
      disposition: "game",
      submissionId: "browser-request-1",
    });
    expect(updates).toEqual([
      {
        epoch: 3,
        revision: 1,
        replace: true,
        entries: [{ id: 1, text: "skills" }],
      },
      {
        epoch: 3,
        revision: 2,
        entry: { id: 2, text: "look" },
      },
      {
        epoch: 3,
        revision: 2,
        entry: { id: 2, text: "look" },
      },
    ]);
  });

  it("returns a successful connection separately from a credential-save warning", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({
      connected: true,
      credentialSaveRequested: true,
      credentialsSaved: false,
      warning: "Connected, but the account was not remembered.",
      accountState: {
        accounts: [],
        credentialStore: {
          backend: "keyring",
          available: false,
          canStore: true,
          message: "The system keyring is unavailable.",
        },
      },
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    const transport = new WebTransport();
    const result = await transport.invoke<any>("ConnectNew", undefined, "alice", "password", true);

    expect(result.connected).toBe(true);
    expect(result.credentialsSaved).toBe(false);
    expect(result.warning).toContain("not remembered");
    expect(result.accountState.credentialStore.available).toBe(false);
  });

  it("projects credential-store health with account snapshots", async () => {
    const transport = new WebTransport();
    const updates: any[] = [];
    transport.subscribe({
      events: () => {},
      system: (update) => {
        if (update.type === "accounts") updates.push(update);
      },
    });

    await (transport as any).handleEnvelope({
      type: "snapshot",
      protocol: 1,
      serverId: "server-a",
      sequence: 1,
      accounts: [],
      credentialStore: {
        backend: "encrypted_file",
        available: false,
        canStore: true,
        message: "Encrypted credential storage is unavailable.",
      },
    });

    expect(updates).toEqual([{
      type: "accounts",
      accounts: [],
      credentialStore: {
        backend: "encrypted_file",
        available: false,
        canStore: true,
        message: "Encrypted credential storage is unavailable.",
      },
    }]);
  });

  it("refreshes a rejected CSRF token and retries the mutation exactly once", async () => {
    const requests: Array<{ url: string; csrf: string; body: string }> = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const csrf = new Headers(init?.headers).get("X-Praetor-CSRF") ?? "";
      requests.push({ url, csrf, body: String(init?.body ?? "") });
      if (url === "/api/v1/bootstrap") {
        return jsonResponse(bootstrap("csrf-new"));
      }
      if (url === "/api/v1/commands" && csrf === "csrf-old") {
        return apiError(403, "csrf_rejected", "Request verification failed.");
      }
      return jsonResponse({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);

    const transport = initializedTransport("csrf-old");
    await transport.invoke("Send", undefined, "look");

    expect(requests.map(({ url }) => url)).toEqual([
      "/api/v1/commands",
      "/api/v1/bootstrap",
      "/api/v1/commands",
    ]);
    expect(requests.map(({ csrf }) => csrf)).toEqual([
      "csrf-old",
      "",
      "csrf-new",
    ]);
    expect(requests[0].body).toBe(requests[2].body);
  });

  it("never retries a non-CSRF forbidden response", async () => {
    const fetchMock = vi.fn(async () =>
      apiError(403, "origin_rejected", "Request origin rejected."));
    vi.stubGlobal("fetch", fetchMock);

    const transport = initializedTransport("csrf-old");
    await expect(transport.invoke("Send", undefined, "look")).rejects.toThrow(
      "Request origin rejected.",
    );
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("bounds repeated CSRF rejection to one bootstrap and one retry", async () => {
    const urls: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      urls.push(url);
      if (url === "/api/v1/bootstrap") {
        return jsonResponse(bootstrap("csrf-new"));
      }
      return apiError(403, "csrf_rejected", "Request verification failed.");
    });
    vi.stubGlobal("fetch", fetchMock);

    const transport = initializedTransport("csrf-old");
    await expect(transport.invoke("Send", undefined, "look")).rejects.toThrow(
      "Request verification failed.",
    );
    expect(urls).toEqual([
      "/api/v1/commands",
      "/api/v1/bootstrap",
      "/api/v1/commands",
    ]);
  });

  it("does not replay a mutation when recovery discovers a new server process", async () => {
    const urls: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      urls.push(url);
      if (url === "/api/v1/bootstrap") {
        return jsonResponse(bootstrap("csrf-new", "server-b"));
      }
      return apiError(403, "csrf_rejected", "Request verification failed.");
    });
    vi.stubGlobal("fetch", fetchMock);

    const transport = initializedTransport("csrf-old", "server-a");
    await expect(transport.invoke("Send", undefined, "look")).rejects.toThrow(
      "Praetor restarted and is resynchronizing.",
    );
    expect(urls).toEqual([
      "/api/v1/commands",
      "/api/v1/bootstrap",
    ]);
  });

  it("coalesces concurrent CSRF recovery without duplicating mutations", async () => {
    let releaseBootstrap: ((response: Response) => void) | undefined;
    const bootstrapResponse = new Promise<Response>((resolve) => {
      releaseBootstrap = resolve;
    });
    let bootstrapRequests = 0;
    const commandCSRF: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === "/api/v1/bootstrap") {
        bootstrapRequests++;
        return bootstrapResponse;
      }
      const csrf = new Headers(init?.headers).get("X-Praetor-CSRF") ?? "";
      commandCSRF.push(csrf);
      if (csrf === "csrf-old") {
        return apiError(403, "csrf_rejected", "Request verification failed.");
      }
      return jsonResponse({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);

    const transport = initializedTransport("csrf-old");
    const first = transport.invoke("Send", undefined, "look");
    const second = transport.invoke("Send", undefined, "inventory");
    await vi.waitFor(() => {
      expect(commandCSRF).toEqual(["csrf-old", "csrf-old"]);
      expect(bootstrapRequests).toBe(1);
    });
    releaseBootstrap?.(jsonResponse(bootstrap("csrf-new")));

    await Promise.all([first, second]);
    expect(bootstrapRequests).toBe(1);
    expect(commandCSRF).toEqual([
      "csrf-old",
      "csrf-old",
      "csrf-new",
      "csrf-new",
    ]);
  });

  it("enters web authentication when CSRF refresh finds an expired session", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === "/api/v1/bootstrap") {
        return apiError(401, "authentication_required", "Authentication required.");
      }
      return apiError(403, "csrf_rejected", "Request verification failed.");
    });
    vi.stubGlobal("fetch", fetchMock);

    const transport = initializedTransport("csrf-old");
    const updates: string[] = [];
    transport.subscribe({
      events: () => {},
      system: (update) => updates.push(update.type),
    });

    await expect(transport.invoke("Send", undefined, "look")).rejects.toBeInstanceOf(
      WebAuthRequiredError,
    );
    expect(updates).toEqual(["auth-expired"]);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("fails closed when CSRF recovery receives an invalid bootstrap", async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      if (String(input) === "/api/v1/bootstrap") {
        return jsonResponse({ ...bootstrap("", ""), csrf: "", serverId: "" });
      }
      return apiError(403, "csrf_rejected", "Request verification failed.");
    });
    vi.stubGlobal("fetch", fetchMock);

    const transport = initializedTransport("csrf-old");
    const updates: string[] = [];
    transport.subscribe({
      events: () => {},
      system: (update) => updates.push(update.type),
    });

    await expect(transport.invoke("Send", undefined, "look")).rejects.toBeInstanceOf(
      WebAuthRequiredError,
    );
    expect(updates).toEqual(["auth-expired"]);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("preserves the typed login failure instead of treating it as session expiry", async () => {
    const fetchMock = vi.fn(async () =>
      apiError(401, "login_failed", "Authentication failed."));
    vi.stubGlobal("fetch", fetchMock);

    const transport = new WebTransport();
    const updates: string[] = [];
    transport.subscribe({
      events: () => {},
      system: (update) => updates.push(update.type),
    });

    await expect(transport.webLogin("wrong")).rejects.toThrow(
      "Authentication failed.",
    );
    expect(updates).toEqual([]);
  });

  it("refreshes another same-profile tab after login without sharing a token", async () => {
    vi.stubGlobal("window", {});
    vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel);
    const requestCSRF: string[] = [];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url === "/api/v1/auth/login") return jsonResponse({ ok: true });
      if (url === "/api/v1/bootstrap") {
        return jsonResponse(bootstrap("csrf-replacement"));
      }
      requestCSRF.push(
        new Headers(init?.headers).get("X-Praetor-CSRF") ?? "",
      );
      return jsonResponse({ ok: true });
    });
    vi.stubGlobal("fetch", fetchMock);

    const existingTab = initializedTransport("csrf-original");
    const systemUpdates: string[] = [];
    existingTab.subscribe({
      events: () => {},
      system: (update) => systemUpdates.push(update.type),
    });
    const loginTab = new WebTransport();

    await loginTab.webLogin("correct");
    await vi.waitFor(() => {
      expect(systemUpdates).toContain("auth-restored");
    });
    await existingTab.invoke("Send", undefined, "look");

    expect(requestCSRF).toEqual(["csrf-replacement"]);
    expect(FakeBroadcastChannel.messages).toHaveLength(1);
    expect(FakeBroadcastChannel.messages[0]).toMatchObject({
      type: "praetor-session-changed",
      action: "login",
    });
    expect(FakeBroadcastChannel.messages[0]).not.toHaveProperty("csrf");
    expect(FakeBroadcastChannel.messages[0]).not.toHaveProperty("password");
  });

  it("signs out other tabs in the same browser profile", async () => {
    vi.stubGlobal("window", {});
    vi.stubGlobal("BroadcastChannel", FakeBroadcastChannel);
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse({ ok: true })));

    const otherTab = initializedTransport("csrf-current");
    const updates: string[] = [];
    otherTab.subscribe({
      events: () => {},
      system: (update) => updates.push(update.type),
    });
    const signingOutTab = initializedTransport("csrf-current");

    await signingOutTab.webLogout();
    await vi.waitFor(() => {
      expect(updates).toEqual(["auth-expired"]);
    });
  });
});

function initializedTransport(csrf: string, serverId = "server-a"): WebTransport {
  const transport = new WebTransport();
  (transport as any).installBootstrap(bootstrap(csrf, serverId));
  return transport;
}

function bootstrap(csrf: string, serverId = "server-a") {
  return {
    protocol: 1,
    csrf,
    serverId,
    configRevision: 1,
    version: "test",
    debug: false,
    accounts: [],
    modeNames: [],
    hasModes: false,
    config: {},
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function apiError(status: number, code: string, message: string): Response {
  return jsonResponse({ error: { code, message } }, status);
}

class FakeBroadcastChannel {
  static channels = new Set<FakeBroadcastChannel>();
  static messages: unknown[] = [];

  onmessage: ((event: MessageEvent<unknown>) => void) | null = null;

  constructor(readonly name: string) {
    FakeBroadcastChannel.channels.add(this);
  }

  postMessage(message: unknown) {
    FakeBroadcastChannel.messages.push(message);
    for (const channel of FakeBroadcastChannel.channels) {
      if (channel === this || channel.name !== this.name) continue;
      queueMicrotask(() => channel.onmessage?.({ data: message } as MessageEvent));
    }
  }

  close() {
    FakeBroadcastChannel.channels.delete(this);
  }

  static reset() {
    FakeBroadcastChannel.channels.clear();
    FakeBroadcastChannel.messages = [];
  }
}
