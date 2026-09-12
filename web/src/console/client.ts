import { observeJobResponse } from './actionFeedback';
import { t } from "./i18n";
import { scopeURL, trackRequest, clusterEpoch } from "../clusterScope";
import {
  getAuthHeaders,
  isAuthenticated,
  currentUser,
  TOKEN_STORAGE_KEY,
} from "../api";

export async function request<T = any>(
  path: string,
  init: RequestInit = {},
  scope: "cluster" | "global" = "cluster",
): Promise<T> {
  const url=scope === 'global' ? `/api${path}` : scopeURL(`/api${path}`);
  const clusterScoped=url.startsWith('/api/clusters/');
  const controller = new AbortController();
  const release = clusterScoped ? trackRequest(controller) : () => {};
  const epoch = clusterEpoch();
  const timer = setTimeout(
    () => controller.abort(),
    init.method ? 190000 : 30000,
  );
  try {
    const response = await fetch(
      url,
      {
        ...init,
        headers: {
          ...getAuthHeaders(),
          ...(init.body ? { "Content-Type": "application/json" } : {}),
          ...init.headers,
        },
        signal: controller.signal,
      },
    );
    const data = await response.json().catch(() => null);
    if (clusterScoped && epoch !== clusterEpoch())
      throw new DOMException("Cluster changed", "AbortError");
    if (response.status === 401) {
      localStorage.removeItem(TOKEN_STORAGE_KEY);
      isAuthenticated.value = false;
      currentUser.value = { username: "guest", role: "viewer" };
    }
    if (!response.ok)
      throw new Error(
        response.status === 401
          ? t("Требуется вход администратора")
          : data?.error ||
              t("Запрос завершился с ошибкой {0}", [response.status]),
      );
    const jobEndpoint = /\/jobs(?:\/|$)/.test(path);
    if (response.status === 202 || jobEndpoint) {
      const clusterId = clusterScoped ? decodeURIComponent(url.split('/')[3]!) : '';
      observeJobResponse(data, response.status === 202, clusterId, !clusterScoped);
    }
    return data as T;
  } catch (error) {
    if (init.method && !['GET', 'HEAD'].includes(init.method.toUpperCase()) &&
        (error instanceof TypeError || (error instanceof DOMException && error.name === 'AbortError'))) {
      throw new Error(t('Ответ на запрос не получен. Результат неизвестен; проверьте задания и состояние цели перед повтором.'));
    }
    throw error;
  } finally {
    clearTimeout(timer);
    release();
  }
}
export const post = (path: string, body: unknown = {}) =>
  request(path, { method: "POST", body: JSON.stringify(body) });
export const globalRequest = <T = any>(path: string, init: RequestInit = {}) =>
  request<T>(path, init, "global");
export const globalPost = (path: string, body: unknown = {}) =>
  globalRequest(path, { method: "POST", body: JSON.stringify(body) });
export async function downloadAPI(path: string, name: string) {
  if (/^\/backups\/[^/]+\/download$/.test(path)) {
    const epoch = clusterEpoch();
    const url = scopeURL(`/api${path}`);
    await post(path.replace(/\/download$/, "/download-ticket"));
    if (epoch !== clusterEpoch()) return;
    // Let the browser stream to disk. The one-use HttpOnly cookie authorizes
    // this exact URL; neither the JWT nor the archive enters a URL/Blob buffer.
    const link = document.createElement("a");
    link.href = url;
    link.download = name;
    document.body.appendChild(link);
    link.click();
    link.remove();
    return;
  }
  const controller = new AbortController();
  const release = trackRequest(controller);
  const epoch = clusterEpoch();
  try {
    const response = await fetch(scopeURL(`/api${path}`), {
      headers: getAuthHeaders(),
      signal: controller.signal,
    });
    if (!response.ok)
      throw new Error(t("Запрос завершился с ошибкой {0}", [response.status]));
    const blob = await response.blob();
    if (epoch !== clusterEpoch()) return;
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = name;
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  } finally {
    release();
  }
}
export const list = (data: any): any[] =>
  Array.isArray(data)
    ? data
    : data?.pods || data?.events || data?.disks || data?.members || [];
export const display = (value: unknown): string =>
  value === null || value === undefined || value === ""
    ? "—"
    : Array.isArray(value) && value.every(item => typeof item !== 'object')
      ? value.join(', ')
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
