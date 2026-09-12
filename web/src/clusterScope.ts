import { ref } from "vue";

export const selectedCluster = ref(
  localStorage.getItem("talosdeck.cluster") || "",
);
let epoch = 0;
const pending = new Set<AbortController>();
export const clusterEpoch = () => epoch;
export function selectCluster(id: string) {
  if (selectedCluster.value === id) return;
  epoch++;
  pending.forEach((controller) => controller.abort());
  pending.clear();
  selectedCluster.value = id;
  if (id) localStorage.setItem("talosdeck.cluster", id);
  else localStorage.removeItem("talosdeck.cluster");
}
export function scopeURL(path: string): string {
  if (
    !path.startsWith("/api/") ||
    /^\/api\/(auth(?:\/|$)|clusters(?:\/|$)|healthz|readyz)/.test(path)
  )
    return path;
  if (!selectedCluster.value) throw new Error("Select a cluster first");
  return `/api/clusters/${encodeURIComponent(selectedCluster.value)}${path.slice(4)}`;
}
export function trackRequest(controller: AbortController) {
  pending.add(controller);
  return () => pending.delete(controller);
}
export function clusterWebSocket(path: string) {
  if (!selectedCluster.value) throw new Error("Select a cluster first");
  return `/api/clusters/${encodeURIComponent(selectedCluster.value)}/ws${path}`;
}
