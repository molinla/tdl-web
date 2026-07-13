/** Bounded LRU map; optional onEvict runs when entries are dropped. */
export class LRUMap<K, V> {
  private map = new Map<K, V>();

  constructor(
    private maxSize: number,
    private onEvict?: (key: K, value: V) => void,
  ) {}

  get size(): number {
    return this.map.size;
  }

  has(key: K): boolean {
    return this.map.has(key);
  }

  get(key: K): V | undefined {
    const value = this.map.get(key);
    if (value === undefined) return undefined;
    this.map.delete(key);
    this.map.set(key, value);
    return value;
  }

  set(key: K, value: V): void {
    if (this.map.has(key)) {
      this.map.delete(key);
    }
    this.map.set(key, value);
    this.trim();
  }

  delete(key: K): boolean {
    const value = this.map.get(key);
    if (value === undefined) return false;
    this.map.delete(key);
    this.onEvict?.(key, value);
    return true;
  }

  /** Drop entries whose keys are not in keep; evicts the rest. */
  retainKeys(keep: ReadonlySet<K>): void {
    for (const key of [...this.map.keys()]) {
      if (keep.has(key)) continue;
      const value = this.map.get(key)!;
      this.map.delete(key);
      this.onEvict?.(key, value);
    }
  }

  private trim(): void {
    while (this.map.size > this.maxSize) {
      const oldest = this.map.keys().next().value as K | undefined;
      if (oldest === undefined) break;
      const value = this.map.get(oldest)!;
      this.map.delete(oldest);
      this.onEvict?.(oldest, value);
    }
  }
}

/** Bounded LRU set for cheap membership tracking. */
export class LRUSet<K> {
  private map = new Map<K, null>();

  constructor(private maxSize: number) {}

  has(key: K): boolean {
    if (!this.map.has(key)) return false;
    this.map.delete(key);
    this.map.set(key, null);
    return true;
  }

  add(key: K): void {
    if (this.map.has(key)) this.map.delete(key);
    this.map.set(key, null);
    while (this.map.size > this.maxSize) {
      const oldest = this.map.keys().next().value as K | undefined;
      if (oldest === undefined) break;
      this.map.delete(oldest);
    }
  }
}
