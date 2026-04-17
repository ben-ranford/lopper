export interface InFlightRefresh<TValue> {
  key: string;
  runId: number;
  promise: Promise<TValue>;
}

export interface CachedRefresh<TValue> {
  key: string;
  inputVersion: number;
  savedAt: number;
  value: TValue;
}

interface FolderRefreshSession<TCachedValue, TInFlightValue> {
  inputVersion: number;
  latestRunId: number;
  nextRunId: number;
  inFlightByKey: Map<string, InFlightRefresh<TInFlightValue>>;
  cacheByKey: Map<string, CachedRefresh<TCachedValue>>;
}

export class RefreshSessionStore<TCachedValue, TInFlightValue = void> {
  private readonly sessionsByFolder = new Map<string, FolderRefreshSession<TCachedValue, TInFlightValue>>();

  bumpInputVersion(folderKey: string): number {
    const session = this.session(folderKey);
    session.inputVersion += 1;
    session.cacheByKey.clear();
    return session.inputVersion;
  }

  reserveRun(folderKey: string): number {
    const session = this.session(folderKey);
    const runId = session.nextRunId + 1;
    session.nextRunId = runId;
    session.latestRunId = runId;
    return runId;
  }

  latestRunId(folderKey: string): number {
    return this.session(folderKey).latestRunId;
  }

  isLatestRun(folderKey: string, runId: number): boolean {
    return this.latestRunId(folderKey) === runId;
  }

  inFlight(folderKey: string, key: string): InFlightRefresh<TInFlightValue> | undefined {
    return this.session(folderKey).inFlightByKey.get(key);
  }

  setInFlight(folderKey: string, key: string, runId: number, promise: Promise<TInFlightValue>): void {
    this.session(folderKey).inFlightByKey.set(key, { key, runId, promise });
  }

  clearInFlight(folderKey: string, key: string, runId?: number): void {
    const session = this.session(folderKey);
    const existing = session.inFlightByKey.get(key);
    if (!existing) {
      return;
    }
    if (runId !== undefined && existing.runId !== runId) {
      return;
    }
    session.inFlightByKey.delete(key);
  }

  getCache(folderKey: string, key: string): CachedRefresh<TCachedValue> | undefined {
    const session = this.session(folderKey);
    const cached = session.cacheByKey.get(key);
    if (!cached) {
      return undefined;
    }
    if (cached.inputVersion !== session.inputVersion) {
      session.cacheByKey.delete(key);
      return undefined;
    }
    return cached;
  }

  setCache(folderKey: string, key: string, value: TCachedValue): CachedRefresh<TCachedValue> {
    const session = this.session(folderKey);
    const cached: CachedRefresh<TCachedValue> = {
      key,
      inputVersion: session.inputVersion,
      savedAt: Date.now(),
      value,
    };
    session.cacheByKey.set(key, cached);
    return cached;
  }

  clearFolder(folderKey: string): void {
    this.sessionsByFolder.delete(folderKey);
  }

  private session(folderKey: string): FolderRefreshSession<TCachedValue, TInFlightValue> {
    let session = this.sessionsByFolder.get(folderKey);
    if (!session) {
      session = {
        inputVersion: 0,
        latestRunId: 0,
        nextRunId: 0,
        inFlightByKey: new Map<string, InFlightRefresh<TInFlightValue>>(),
        cacheByKey: new Map<string, CachedRefresh<TCachedValue>>(),
      };
      this.sessionsByFolder.set(folderKey, session);
    }
    return session;
  }
}
