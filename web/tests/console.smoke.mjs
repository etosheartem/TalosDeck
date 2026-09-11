import { chromium } from "playwright";
import assert from "node:assert/strict";
import { createServer } from "vite";
import { mkdtemp, mkdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
const artifacts =
  process.env.UI_SCREENSHOT_DIR || (await mkdtemp(join(tmpdir(), "talos-ui-")));
await mkdir(artifacts, { recursive: true });
const server = await createServer({
  server: { host: "127.0.0.1", port: 5175, strictPort: true },
});
await server.listen();
const browser = await chromium.launch({
  executablePath: process.env.CHROMIUM_PATH || "/usr/bin/chromium",
  headless: true,
  args: ["--no-sandbox"],
});
try {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 1050 },
  });
  const errors = [];
  const mutations = [];
  const nodes = [
    {
      hostname: "talos-cp-01",
      ip: "10.42.0.110",
      role: "controlplane",
      ready: true,
      cpuUsage: 17.3,
      memoryUsage: "2.1 / 8 GiB",
      version: "v1.14.0",
      uptime: "14d 6h",
    },
    {
      hostname: "talos-worker-01",
      ip: "10.42.0.111",
      role: "worker",
      ready: true,
      cpuUsage: 42.6,
      memoryUsage: "5.8 / 16 GiB",
      version: "v1.14.0",
      uptime: "14d 6h",
    },
    {
      hostname: "talos-worker-02",
      ip: "10.42.0.112",
      role: "worker",
      ready: false,
      cpuUsage: 0,
      memoryUsage: "—",
      version: "v1.14.0",
      uptime: "—",
    },
  ];
  const fixtures = {
    "/api/nodes": nodes,
    "/api/cluster": {
      name: "production-eu-01",
      endpoint: "https://10.42.0.110:6443",
      talosVersion: "v1.14.0",
      kubernetesVersion: "v1.32.2",
    },
    "/api/k8s/pods": [
      {
        id: "p1",
        name: "coredns-8c66d6799-2txbh",
        namespace: "kube-system",
        status: "Running",
        nodeName: "talos-worker-01",
        readyContainers: "1/1",
        restarts: 0,
        ip: "10.244.1.7",
        age: "14d",
      },
      {
        id: "p2",
        name: "api-gateway-74bdf96d-ltf89",
        namespace: "production",
        status: "CrashLoopBackOff",
        nodeName: "talos-worker-02",
        readyContainers: "0/1",
        restarts: 7,
        ip: "10.244.2.3",
        age: "6h",
      },
    ],
    "/api/cluster/etcd": {
      healthy: true,
      leaderName: "talos-cp-01",
      totalDbSize: "24 MiB",
      raftTerm: 4,
      raftIndex: 218441,
      members: [
        {
          id: "1",
          name: "talos-cp-01",
          healthy: true,
          leader: true,
          dbSize: "24 MiB",
          peerUrls: ["https://10.42.0.110:2380"],
        },
      ],
      alarms: [],
    },
    "/api/proxmox/status": {
      configured: true,
      node: "pve-01",
      status: {
        cpuUsagePercent: 22,
        memory: { available: 18000000000 },
        storage: { free: 380000000000 },
      },
    },
    "/api/auth/me": {
      authenticated: true,
      user: { username: "admin", role: "admin" },
    },
    "/api/auth/login": {
      token: "fixture-test-token",
      user: { username: "admin", role: "admin" },
    },
    "/api/proxmox/next-vmid": { vmid: 114 },
    "/api/backups": [
      {
        id: "b1",
        filename: "etcd-2026-09-11.snapshot",
        type: "etcd",
        humanSize: "24 MiB",
        timestamp: "2026-09-11T09:00:00Z",
        node: "talos-cp-01",
      },
    ],
    "/api/alerts/config": {
      enabled: true,
      bot_configured: true,
      chat_id: "-10012345678",
      min_level: "WARNING",
    },
    "/api/audit": [
      {
        id: "a1",
        action: "node.reboot",
        user: "admin",
        status: "success",
        ip: "192.168.88.10",
        timestamp: "2026-09-11T10:00:00Z",
        details: { node: "10.42.0.111" },
      },
    ],
  };
  await context.route("**/api/**", async (route) => {
    const req = route.request();
    const p = new URL(req.url()).pathname;
    if (!p.startsWith("/api/")) return route.continue();
    if (req.method() !== "GET") mutations.push(p);
    let body = fixtures[p] || { success: true };
    if (p.endsWith("/disks"))
      body = [
        {
          devicePath: "/dev/sda",
          model: "QEMU HARDDISK",
          prettySize: "100 GiB",
          size: 107374182400,
          type: "Virtual",
          bus: "SCSI",
          partitions: [
            {
              location: "/dev/sda1",
              id: "STATE",
              prettySize: "1 GiB",
              size: 1073741824,
              filesystem: "xfs",
              mountPath: "/system/state",
              used: "280 MiB",
            },
          ],
        },
      ];
    if (p.endsWith("/config") && p.includes("/nodes/"))
      body = {
        configYaml:
          "version: v1alpha1\nmachine:\n  type: controlplane\n  network:\n    hostname: talos-cp-01\ncluster:\n  clusterName: production-eu-01",
      };
    if (p.endsWith("/services"))
      body = [
        {
          id: "kubelet",
          state: "Running",
          healthy: true,
          description: "Kubernetes node agent",
        },
      ];
    if (p.endsWith("/containers"))
      body = [
        {
          id: "container-1",
          name: "kubelet",
          status: "Running",
          image: "registry.k8s.io/kubelet:v1.32.2",
        },
      ];
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });
  await context.routeWebSocket("**/ws/**", (ws) => {
    ws.send("kernel: test stream connected");
  });
  const page = await context.newPage();
  page.on("pageerror", (e) => errors.push(String(e)));
  await page.goto("http://127.0.0.1:5175");
  await page.getByRole("button", { name: "Открыть список нод" }).waitFor();
  await page.waitForTimeout(300);
  await page.screenshot({
    path: artifacts + "/console-overview.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Войти", exact: true }).click();
  await page.getByLabel("Пароль", { exact: true }).fill("test");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Войти", exact: true })
    .click();
  await page.waitForTimeout(200);
  for (const id of [
    "nodes",
    "workloads",
    "storage",
    "config",
    "etcd",
    "backups",
    "maintenance",
    "audit",
    "settings",
  ]) {
    await page.goto(`http://127.0.0.1:5175/#${id}`);
    await page.waitForTimeout(220);
    assert.equal(await page.locator(".access-state").count(), 0);
    await page.screenshot({
      path: `${artifacts}/console-${id}.png`,
      fullPage: true,
    });
  }
  await page.goto("http://127.0.0.1:5175/#nodes");
  await page
    .getByRole("button", { name: "talos-cp-01", exact: true })
    .waitFor();
  await page.getByPlaceholder("Поиск по всем полям…").fill("worker-01");
  assert.equal(await page.locator("tbody tr").count(), 1);
  await page.getByPlaceholder("Поиск по всем полям…").fill("");
  await page.getByRole("button", { name: "talos-cp-01", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "kubelet", exact: true })
    .waitFor();
  await page.screenshot({
    path: artifacts + "/console-inspector.png",
    fullPage: true,
  });
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Живые логи", exact: true })
    .click();
  await page
    .getByText("kernel: test stream connected", { exact: true })
    .waitFor();
  await page.getByRole("button", { name: "Пауза", exact: true }).click();
  await page.getByRole("button", { name: "Продолжить", exact: true }).waitFor();
  await page.keyboard.press("Escape");
  assert.equal(await page.getByRole("dialog").count(), 0);
  await page
    .getByRole("button", { name: "Добавить worker", exact: true })
    .click();
  await page.getByLabel("Имя", { exact: true }).fill("worker-test");
  await page.getByRole("button", { name: "Создать машину" }).click();
  await page.waitForTimeout(200);
  assert(mutations.includes("/api/proxmox/worker"));
  await page.goto("http://127.0.0.1:5175/#maintenance");
  await page
    .getByRole("button", { name: "Перезагрузить", exact: true })
    .click();
  assert(!mutations.some((p) => p.endsWith("/reboot")));
  await page.getByRole("button", { name: "Отмена", exact: true }).click();
  assert(!mutations.some((p) => p.endsWith("/reboot")));
  await page.goto("http://127.0.0.1:5175/#settings");
  await page.getByRole("button", { name: "Сохранить", exact: true }).click();
  await page.waitForTimeout(100);
  assert(mutations.includes("/api/alerts/config"));
  await page.goto("http://127.0.0.1:5175/#storage");
  await page.getByRole("button", { name: "/dev/sda", exact: true }).click();
  await page
    .getByRole("cell", { name: "/system/state", exact: true })
    .waitFor();
  await page.keyboard.press("Escape");
  await page.goto("http://127.0.0.1:5175/#etcd");
  await page
    .getByRole("cell", { name: '["https://10.42.0.110:2380"]', exact: true })
    .waitFor();
  await page.setViewportSize({ width: 390, height: 844 });
  for (const id of ["overview", "nodes", "workloads", "storage", "settings"]) {
    await page.goto(`http://127.0.0.1:5175/#${id}`);
    await page.waitForTimeout(200);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > innerWidth,
    );
    assert(!overflow, `overflow ${id}`);
    await page.screenshot({
      path: `${artifacts}/console-mobile-${id}.png`,
      fullPage: true,
    });
  }
  await page.getByRole("button", { name: "Открыть меню" }).click();
  await page
    .getByRole("navigation")
    .getByRole("button", { name: "Аудит", exact: true })
    .click();
  assert.equal(new URL(page.url()).hash, "#audit");
  const offline = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  await offline.route("**/api/**", (route) =>
    !new URL(route.request().url()).pathname.startsWith("/api/")
      ? route.continue()
      : route.fulfill({
          status: 503,
          body: JSON.stringify({ error: "Offline fixture" }),
          contentType: "application/json",
        }),
  );
  await offline.goto("http://127.0.0.1:5175");
  await offline.getByText("Часть данных недоступна").waitFor();
  assert.equal(
    await offline.getByText("talos-cp-01", { exact: true }).count(),
    0,
  );
  await offline.screenshot({
    path: artifacts + "/console-offline.png",
    fullPage: true,
  });
  await offline.goto("http://127.0.0.1:5175/#config");
  await offline
    .getByRole("heading", { name: "Требуется вход", exact: true })
    .waitFor();
  assert.equal(await offline.locator(".code-lines").count(), 0);
  assert.deepEqual(errors, []);
  console.log(
    "PASS: 10 sections, auth, table search, inspector, WebSocket logs, pause, disk normalization, etcd URLs, protected config, escape, provisioning, confirmation cancellation, settings, mobile navigation, no page overflow, offline/no mock fallback.",
  );
  console.log("Screenshots:", artifacts);
} finally {
  await browser.close();
  await server.close();
}
