import {
  getAuthHeaders,
  isAuthenticated,
  currentUser,
  TOKEN_STORAGE_KEY,
} from "../api";

export async function request<T = any>(
  path: string,
  init: RequestInit = {},
): Promise<T> {
  const controller = new AbortController();
  const timer = setTimeout(
    () => controller.abort(),
    init.method ? 190000 : 30000,
  );
  try {
    const response = await fetch(`/api${path}`, {
      ...init,
      headers: {
        ...getAuthHeaders(),
        ...(init.body ? { "Content-Type": "application/json" } : {}),
        ...init.headers,
      },
      signal: controller.signal,
    });
    const data = await response.json().catch(() => null);
    if (response.status === 401) {
      localStorage.removeItem(TOKEN_STORAGE_KEY);
      isAuthenticated.value = false;
      currentUser.value = { username: "guest", role: "viewer" };
    }
    if (!response.ok)
      throw new Error(
        response.status === 401
          ? "Требуется вход администратора"
          : data?.error || `Запрос завершился с ошибкой ${response.status}`,
      );
    return data as T;
  } finally {
    clearTimeout(timer);
  }
}
export const post = (path: string, body: unknown = {}) =>
  request(path, { method: "POST", body: JSON.stringify(body) });
export const list = (data: any): any[] =>
  Array.isArray(data)
    ? data
    : data?.pods || data?.events || data?.disks || data?.members || [];
export const display = (value: unknown): string =>
  value === null || value === undefined || value === ""
    ? "—"
    : typeof value === "object"
      ? JSON.stringify(value)
      : String(value);
export const bytes = (n?: number) =>
  n == null
    ? "—"
    : n < 1024 ** 3
      ? `${(n / 1024 ** 2).toFixed(1)} MiB`
      : `${(n / 1024 ** 3).toFixed(1)} GiB`;
export function download(text: string, name: string) {
  const url = URL.createObjectURL(
    new Blob([text], { type: "text/plain;charset=utf-8" }),
  );
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

export function normalizeDisk(raw: any) {
  return {
    ...raw,
    name: raw.name || raw.devicePath || raw.deviceName || "—",
    size:
      raw.prettySize ||
      (typeof raw.size === "number" ? bytes(raw.size) : raw.size),
    status:
      raw.healthy === true
        ? "Healthy"
        : raw.healthy === false
          ? "Failed"
          : "Unknown",
    partitions: (raw.partitions || []).map((p: any) => ({
      ...p,
      device: p.device || p.location || p.id,
      label: p.label || p.id,
      mountpoint: p.mountpoint || p.mountPath,
      size:
        p.prettySize || (typeof p.size === "number" ? bytes(p.size) : p.size),
      used: p.used || (p.usedBytes != null ? bytes(p.usedBytes) : "—"),
    })),
  };
}
